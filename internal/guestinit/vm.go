package guestinit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"text/template"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
)

// VMConfig holds what goes into a Firecracker config file. Paths name files
// inside the jailer's chroot, as plain names. Which disks a VM takes depends
// on its mode, and the order of the drives fixes the guest's /dev/vdX names
// (see the drive constants in review.go and build.go).
type VMConfig struct {
	Mode   Mode
	Kernel string
	Initrd string // build only: the one-file initramfs holding cc-guestinit

	RootDisk   string // review and selftest: the built guest image
	ConfigDisk string
	CodeDisk   string // review only
	OutDisk    string // review and selftest

	BaseDisk   string // build only: the Ubuntu squashfs
	InputsDisk string // build only
	TargetDisk string // build only: the blank disk that becomes RootDisk

	VCPUs  int
	MemMiB int
	Tap    string // review and selftest; the build VM has no network device
}

// bootArgs leave out pci=off and the virtio_mmio devices, which Firecracker
// adds itself (spike S2). reboot=k makes the guest's reboot end the VM;
// panic=1 does the same after a kernel panic.
const baseBootArgs = "console=ttyS0 reboot=k panic=1 quiet"

var (
	vmFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	vmTapPattern  = regexp.MustCompile(`^cc-tap[0-9]{1,3}$`)
)

type vmDrive struct {
	ID, Path       string
	Root, ReadOnly bool
}

// RenderVM fills the Firecracker config template for c.
func RenderVM(c VMConfig) ([]byte, error) {
	drives, bootArgs, err := c.layout()
	if err != nil {
		return nil, err
	}
	if c.VCPUs < 1 || c.VCPUs > 8 {
		return nil, fmt.Errorf("vcpu count %d: want 1 to 8", c.VCPUs)
	}
	if c.MemMiB < 256 || c.MemMiB > 16384 {
		return nil, fmt.Errorf("memory %d MiB: want 256 to 16384", c.MemMiB)
	}
	for _, f := range append([]string{c.Kernel}, pathsOf(drives)...) {
		if !vmFilePattern.MatchString(f) {
			return nil, fmt.Errorf("file %q: want a plain name inside the jail", f)
		}
	}
	if c.Initrd != "" && !vmFilePattern.MatchString(c.Initrd) {
		return nil, fmt.Errorf("initrd %q: want a plain name inside the jail", c.Initrd)
	}

	t, err := template.New("vm.json.tmpl").Option("missingkey=error").Funcs(template.FuncMap{
		"json": func(s string) (string, error) {
			b, err := json.Marshal(s)
			return string(b), err
		},
	}).ParseFS(assets.Guest, "guest/vm.json.tmpl")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, struct {
		VMConfig
		BootArgs string
		Drives   []vmDrive
	}{c, bootArgs, drives})
	if err != nil {
		return nil, err
	}
	if !json.Valid(buf.Bytes()) {
		return nil, errors.New("vm.json.tmpl rendered invalid JSON")
	}
	return buf.Bytes(), nil
}

// layout picks the drives, in guest order, and the boot arguments for the
// mode, refusing any disk, initrd or tap the mode doesn't take.
func (c VMConfig) layout() ([]vmDrive, string, error) {
	need := func(name, v string) error {
		if v == "" {
			return fmt.Errorf("mode %s needs %s", c.Mode, name)
		}
		return nil
	}
	none := func(fields map[string]string) error {
		for name, v := range fields {
			if v != "" {
				return fmt.Errorf("mode %s takes no %s", c.Mode, name)
			}
		}
		return nil
	}
	switch c.Mode {
	case ModeReview, ModeSelftest:
		if err := none(map[string]string{"initrd": c.Initrd, "base disk": c.BaseDisk,
			"inputs disk": c.InputsDisk, "target disk": c.TargetDisk}); err != nil {
			return nil, "", err
		}
		if c.Mode == ModeSelftest {
			if err := none(map[string]string{"code disk": c.CodeDisk}); err != nil {
				return nil, "", err
			}
		}
		for name, v := range map[string]string{"a kernel": c.Kernel, "a root disk": c.RootDisk,
			"a config disk": c.ConfigDisk, "an output disk": c.OutDisk} {
			if err := need(name, v); err != nil {
				return nil, "", err
			}
		}
		if !vmTapPattern.MatchString(c.Tap) {
			return nil, "", fmt.Errorf("tap %q: want cc-tap and a number", c.Tap)
		}
		drives := []vmDrive{{"root", c.RootDisk, true, true}, {"config", c.ConfigDisk, false, true}}
		if c.Mode == ModeReview {
			if err := need("a code disk", c.CodeDisk); err != nil {
				return nil, "", err
			}
			drives = append(drives, vmDrive{"code", c.CodeDisk, false, true})
		}
		drives = append(drives, vmDrive{"out", c.OutDisk, false, false})
		return drives, baseBootArgs + " rootfstype=ext4 init=/usr/local/sbin/cc-guestinit", nil
	case ModeBuild:
		if err := none(map[string]string{"root disk": c.RootDisk, "code disk": c.CodeDisk,
			"output disk": c.OutDisk, "tap": c.Tap}); err != nil {
			return nil, "", err
		}
		for name, v := range map[string]string{"a kernel": c.Kernel, "an initrd": c.Initrd,
			"a base disk": c.BaseDisk, "a config disk": c.ConfigDisk,
			"an inputs disk": c.InputsDisk, "a target disk": c.TargetDisk} {
			if err := need(name, v); err != nil {
				return nil, "", err
			}
		}
		return []vmDrive{
			{"base", c.BaseDisk, false, true},
			{"config", c.ConfigDisk, false, true},
			{"inputs", c.InputsDisk, false, true},
			{"target", c.TargetDisk, false, false},
		}, baseBootArgs, nil
	default:
		return nil, "", fmt.Errorf("mode %q: want build, review or selftest", c.Mode)
	}
}

func pathsOf(ds []vmDrive) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Path
	}
	return out
}
