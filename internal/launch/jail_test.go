package launch

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// fakeMounter records bind mounts instead of making them.
type fakeMounter struct {
	binds    [][2]string
	unmounts []string
}

func (m *fakeMounter) BindRO(src, dst string) error {
	m.binds = append(m.binds, [2]string{src, dst})
	return nil
}

func (m *fakeMounter) Unmount(path string) error {
	m.unmounts = append(m.unmounts, path)
	return nil
}

func stageRig(t *testing.T, cfg guestinit.Config) (*rig, *fakeMounter, Job) {
	t.Helper()
	r := newRig(t, cfg)
	r.env.JailUID, r.env.JailGID = os.Getuid(), os.Getgid()
	r.run = &system.FakeRunner{}
	r.run.On("mkfs.ext4").Return(system.Result{})
	r.env.Run = r.run
	return r, &fakeMounter{}, Job{ID: r.id, Config: cfg}
}

func TestStageReview(t *testing.T) {
	r, m, job := stageRig(t, reviewCfg())
	j, err := Stage(context.Background(), r.env, m, job)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	wantRoot := filepath.Join(r.env.Paths.JailerBase, "firecracker", r.id.String(), "root")
	if j.Root != wantRoot {
		t.Errorf("Root = %s, want %s", j.Root, wantRoot)
	}

	// The shared images go in read-only, by bind mount, never copied.
	wantBinds := [][2]string{
		{r.env.Paths.Kernel, filepath.Join(wantRoot, "vmlinux")},
		{r.env.Paths.RootDisk, filepath.Join(wantRoot, "rootfs.ext4")},
	}
	if !slices.Equal(m.binds, wantBinds) {
		t.Errorf("binds = %v, want %v", m.binds, wantBinds)
	}

	// The run's own disks are copies, read-only to the jail user.
	for _, name := range []string{"config.img", "code.ext4"} {
		src, _ := os.ReadFile(filepath.Join(r.dir, name))
		dst, err := os.ReadFile(filepath.Join(wantRoot, name))
		if err != nil || !bytes.Equal(src, dst) {
			t.Errorf("%s not copied: %v", name, err)
		}
		if fi, _ := os.Stat(filepath.Join(wantRoot, name)); fi.Mode().Perm() != 0o444 {
			t.Errorf("%s mode %v, want 0444", name, fi.Mode().Perm())
		}
	}

	// A fresh output disk, writable by the jail user alone.
	out := filepath.Join(wantRoot, "out.ext4")
	fi, err := os.Stat(out)
	if err != nil || fi.Size() != OutDiskSize || fi.Mode().Perm() != 0o600 {
		t.Errorf("out.ext4: %v, %v; want %d bytes, 0600", fi, err, OutDiskSize)
	}
	calls := r.run.Calls()
	if len(calls) != 1 || calls[0].Name != "mkfs.ext4" || calls[0].Args[len(calls[0].Args)-1] != out || calls[0].Root {
		t.Errorf("mkfs calls = %+v", calls)
	}

	want, _ := guestinit.RenderVM(guestinit.VMConfig{
		Mode: guestinit.ModeReview, Kernel: "vmlinux", RootDisk: "rootfs.ext4", ConfigDisk: "config.img",
		CodeDisk: "code.ext4", OutDisk: "out.ext4", VCPUs: 2, MemMiB: 2048, Tap: "cc-tap0",
	})
	if got, _ := os.ReadFile(filepath.Join(wantRoot, "vm.json")); !bytes.Equal(got, want) {
		t.Errorf("vm.json =\n%s\nwant\n%s", got, want)
	}
}

func TestStageBuild(t *testing.T) {
	r, m, job := stageRig(t, guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"})
	j, err := Stage(context.Background(), r.env, m, job)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(m.binds) != 0 {
		t.Errorf("a build bound %v; its inputs come from the run folder", m.binds)
	}
	for _, name := range []string{"vmlinux", "init.cpio", "base.squashfs", "inputs.ext4", "config.img"} {
		if _, err := os.Stat(filepath.Join(j.Root, name)); err != nil {
			t.Errorf("%s not staged: %v", name, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(j.Root, "rootfs.ext4")); err != nil || fi.Size() != TargetDiskSize {
		t.Errorf("target disk: %v, %v", fi, err)
	}
	vm, _ := os.ReadFile(filepath.Join(j.Root, "vm.json"))
	if strings.Contains(string(vm), "network-interfaces") || !strings.Contains(string(vm), `"initrd_path": "init.cpio"`) {
		t.Errorf("build vm.json:\n%s", vm)
	}
}

func TestStageRefusesALeftoverJail(t *testing.T) {
	r, m, job := stageRig(t, reviewCfg())
	dir := filepath.Join(r.env.Paths.JailerBase, "firecracker", r.id.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), r.env, m, job); err == nil {
		t.Error("Stage reused a jail folder that already existed")
	}
}

func TestArgvGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  guestinit.Config
	}{
		{"argv-review.txt", reviewCfg()},
		{"argv-build.txt", guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultPaths()
			id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
			j := &Jail{Dir: filepath.Join(p.JailerBase, "firecracker", id.String()), Unit: "codecheck-" + id.String() + ".scope"}
			argv := j.Argv(Env{Paths: p, JailUID: 973, JailGID: 973}, Job{ID: id, Config: tc.cfg})
			got := []byte(strings.Join(argv, "\n") + "\n")
			golden := filepath.Join("testdata", "golden", tc.name)
			if *update {
				os.MkdirAll(filepath.Dir(golden), 0o755)
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("argv differs from %s:\n%s", golden, got)
			}
		})
	}
}

// eventsProc serves cgroup.events from a script, one reading per call.
type eventsProc struct {
	*fakeProc
	mu       sync.Mutex
	path     string
	readings []string // "" means the scope has gone
	reads    int
}

func (p *eventsProc) ReadFile(path string) ([]byte, error) {
	if path != p.path {
		return p.fakeProc.ReadFile(path)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.readings[min(p.reads, len(p.readings)-1)]
	p.reads++
	if s == "" {
		return nil, os.ErrNotExist
	}
	return []byte(s), nil
}

func waitRig(t *testing.T, readings ...string) (Env, *Jail, *eventsProc, *system.FakeRunner) {
	t.Helper()
	p := DefaultPaths()
	p.CgroupRoot = "/cg"
	j := &Jail{Unit: "codecheck-cc-20261121-101500-a1b2c3.scope"}
	ep := &eventsProc{fakeProc: proxyListening(), path: "/cg/system.slice/" + j.Unit + "/cgroup.events", readings: readings}
	run := &system.FakeRunner{}
	return Env{Paths: p, Proc: ep, Run: run}, j, ep, run
}

func TestWaitReturnsOnceTheScopeEmpties(t *testing.T) {
	e, j, ep, _ := waitRig(t, "populated 1\nfrozen 0\n", "populated 1\nfrozen 0\n", "populated 0\nfrozen 0\n")
	if err := j.Wait(context.Background(), e, time.Minute, time.Millisecond); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if ep.reads != 3 {
		t.Errorf("read cgroup.events %d times, want 3: Wait returned before populated 0", ep.reads)
	}
}

func TestWaitTreatsAGoneScopeAsEmpty(t *testing.T) {
	e, j, _, _ := waitRig(t, "populated 1\n", "")
	if err := j.Wait(context.Background(), e, time.Minute, time.Millisecond); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestWaitKillsTheScopeAfterTheTimeout(t *testing.T) {
	// The scope never empties, even after the kill: Wait still gives up.
	e, j, _, run := waitRig(t, "populated 1\n")
	run.On("systemctl", "kill", "--signal=SIGKILL", j.Unit).Return(system.Result{})
	defer func(g time.Duration) { killGrace = g }(killGrace)
	killGrace = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- j.Wait(context.Background(), e, 20*time.Millisecond, time.Millisecond) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait never returned after killing a scope that stayed populated")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Wait = %v, want ErrTimeout", err)
	}
	if len(run.Pending()) != 0 {
		t.Error("Wait never killed the scope")
	}
}

func TestCleanupUnmountsBeforeRemoving(t *testing.T) {
	r, m, job := stageRig(t, reviewCfg())
	j, err := Stage(context.Background(), r.env, m, job)
	if err != nil {
		t.Fatal(err)
	}
	r.proc.files["/proc/self/mountinfo"] = "22 1 0:21 / /proc rw - proc proc rw\n"
	if err := j.Cleanup(r.env, m); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	for _, b := range m.binds {
		if !slices.Contains(m.unmounts, b[1]) {
			t.Errorf("%s left mounted", b[1])
		}
	}
	if _, err := os.Stat(j.Dir); !os.IsNotExist(err) {
		t.Errorf("jail folder still there: %v", err)
	}
}

func TestCleanupKeepsAJailThatStillHoldsAMount(t *testing.T) {
	r, m, job := stageRig(t, reviewCfg())
	j, err := Stage(context.Background(), r.env, m, job)
	if err != nil {
		t.Fatal(err)
	}
	// Removing files under a live bind mount could reach the shared images.
	r.proc.files["/proc/self/mountinfo"] = "40 22 8:2 /var/lib/codecheck-sandbox/rootfs.ext4 " +
		filepath.Join(j.Root, "rootfs.ext4") + " ro - ext4 /dev/sda2 rw\n"
	err = j.Cleanup(r.env, m)
	if err == nil {
		t.Fatal("Cleanup removed a jail with a mount inside")
	}
	if _, serr := os.Stat(j.Root); serr != nil {
		t.Errorf("jail removed anyway: %v", serr)
	}
}
