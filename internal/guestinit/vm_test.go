package guestinit

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func vmConfigs() map[string]VMConfig {
	return map[string]VMConfig{
		"vm-review.json": {
			Mode: ModeReview, Kernel: "vmlinux", RootDisk: "rootfs.ext4", ConfigDisk: "config.img",
			CodeDisk: "code.ext4", OutDisk: "out.ext4", VCPUs: 2, MemMiB: 2048, Tap: "cc-tap0",
		},
		"vm-selftest.json": {
			Mode: ModeSelftest, Kernel: "vmlinux", RootDisk: "rootfs.ext4", ConfigDisk: "config.img",
			CodeDisk: "code.ext4", OutDisk: "out.ext4", VCPUs: 1, MemMiB: 512, Tap: "cc-tap0",
		},
		"vm-build.json": {
			Mode: ModeBuild, Kernel: "vmlinux", Initrd: "init.cpio", BaseDisk: "base.squashfs",
			ConfigDisk: "config.img", InputsDisk: "inputs.ext4", TargetDisk: "rootfs.ext4", VCPUs: 2, MemMiB: 1024,
		},
	}
}

func TestRenderVMGolden(t *testing.T) {
	for name, c := range vmConfigs() {
		t.Run(name, func(t *testing.T) {
			got, err := RenderVM(c)
			if err != nil {
				t.Fatalf("RenderVM: %v", err)
			}
			golden := filepath.Join("testdata", "golden", name)
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading %s: %v", golden, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the golden file:\n%s", name, got)
			}
		})
	}
}

// fcConfig mirrors the parts of Firecracker's config file the tests check.
type fcConfig struct {
	BootSource struct {
		Kernel   string `json:"kernel_image_path"`
		BootArgs string `json:"boot_args"`
		Initrd   string `json:"initrd_path"`
	} `json:"boot-source"`
	Drives []struct {
		ID       string `json:"drive_id"`
		Path     string `json:"path_on_host"`
		Root     bool   `json:"is_root_device"`
		ReadOnly bool   `json:"is_read_only"`
	} `json:"drives"`
	Machine struct {
		VCPUs  int `json:"vcpu_count"`
		MemMiB int `json:"mem_size_mib"`
	} `json:"machine-config"`
	Net *[]struct {
		Iface string `json:"iface_id"`
		Host  string `json:"host_dev_name"`
	} `json:"network-interfaces"`
}

func parseVM(t *testing.T, c VMConfig) fcConfig {
	t.Helper()
	b, err := RenderVM(c)
	if err != nil {
		t.Fatalf("RenderVM: %v", err)
	}
	var fc fcConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&fc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, b)
	}
	return fc
}

func TestRenderVMDriveOrderMatchesTheGuest(t *testing.T) {
	type drive struct {
		path         string
		root, rdonly bool
	}
	want := map[string][]drive{
		"vm-review.json":   {{"rootfs.ext4", true, true}, {"config.img", false, true}, {"code.ext4", false, true}, {"out.ext4", false, false}},
		"vm-selftest.json": {{"rootfs.ext4", true, true}, {"config.img", false, true}, {"code.ext4", false, true}, {"out.ext4", false, false}},
		// No root device: the build boots its initramfs.
		"vm-build.json": {{"base.squashfs", false, true}, {"config.img", false, true}, {"inputs.ext4", false, true}, {"rootfs.ext4", false, false}},
	}
	for name, c := range vmConfigs() {
		fc := parseVM(t, c)
		if len(fc.Drives) != len(want[name]) {
			t.Errorf("%s: %d drives, want %d", name, len(fc.Drives), len(want[name]))
			continue
		}
		for i, d := range fc.Drives {
			w := want[name][i]
			if d.Path != w.path || d.Root != w.root || d.ReadOnly != w.rdonly {
				t.Errorf("%s drive %d = %+v, want %+v", name, i, d, w)
			}
		}
	}
}

func TestRenderVMNetworkOnlyForJobs(t *testing.T) {
	for name, c := range vmConfigs() {
		fc := parseVM(t, c)
		if c.Mode == ModeBuild {
			if fc.Net != nil {
				t.Errorf("%s: the build VM has a network-interfaces entry", name)
			}
			continue
		}
		if fc.Net == nil || len(*fc.Net) != 1 || (*fc.Net)[0].Host != "cc-tap0" || (*fc.Net)[0].Iface != "eth0" {
			t.Errorf("%s: network = %+v, want eth0 on cc-tap0", name, fc.Net)
		}
	}
}

func TestRenderVMBootArgs(t *testing.T) {
	for name, c := range vmConfigs() {
		args := parseVM(t, c).BootSource.BootArgs
		// Firecracker adds both itself (spike S2).
		for _, banned := range []string{"pci=off", "virtio_mmio"} {
			if strings.Contains(args, banned) {
				t.Errorf("%s: boot args repeat %s: %q", name, banned, args)
			}
		}
		for _, want := range []string{"reboot=k", "panic=1", "console=ttyS0"} {
			if !strings.Contains(args, want) {
				t.Errorf("%s: boot args lack %s: %q", name, want, args)
			}
		}
		hasInit := strings.Contains(args, "init=/usr/local/sbin/cc-guestinit")
		if c.Mode == ModeBuild && hasInit || c.Mode != ModeBuild && !hasInit {
			t.Errorf("%s: init= wrong for mode %s: %q", name, c.Mode, args)
		}
	}
}

func TestRenderVMRefusesBadConfigs(t *testing.T) {
	good := vmConfigs()["vm-review.json"]
	cases := map[string]func(*VMConfig){
		"quote in a path":    func(c *VMConfig) { c.RootDisk = `root"x.ext4` },
		"absolute path":      func(c *VMConfig) { c.Kernel = "/boot/vmlinux" },
		"parent path":        func(c *VMConfig) { c.CodeDisk = "../code.ext4" },
		"missing code disk":  func(c *VMConfig) { c.CodeDisk = "" },
		"extra build disk":   func(c *VMConfig) { c.BaseDisk = "base.squashfs" },
		"initrd on a review": func(c *VMConfig) { c.Initrd = "init.cpio" },
		"no vcpus":           func(c *VMConfig) { c.VCPUs = 0 },
		"huge memory":        func(c *VMConfig) { c.MemMiB = 1 << 20 },
		"bad tap":            func(c *VMConfig) { c.Tap = "eth0" },
		"unknown mode":       func(c *VMConfig) { c.Mode = "shell" },
	}
	for name, mutate := range cases {
		c := good
		mutate(&c)
		if _, err := RenderVM(c); err == nil {
			t.Errorf("%s: RenderVM accepted it", name)
		}
	}
	b := vmConfigs()["vm-build.json"]
	b.Tap = "cc-tap0"
	if _, err := RenderVM(b); err == nil {
		t.Error("RenderVM gave the build VM a tap")
	}
}
