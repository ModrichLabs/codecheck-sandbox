package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// hostRunner plays every command a run makes: nft, mkfs.ext4, systemd-run,
// systemctl and debugfs (through setpriv).
type hostRunner struct {
	nft   []byte
	disk  *diskRunner
	boot  func() // runs when systemd-run starts the VM
	names []string
}

func (h *hostRunner) Run(ctx context.Context, c system.Cmd) (system.Result, error) {
	h.names = append(h.names, c.Name)
	switch c.Name {
	case "nft":
		return system.Result{Stdout: h.nft}, nil
	case "mkfs.ext4", "systemctl":
		return system.Result{}, nil
	case "systemd-run":
		if h.boot != nil {
			h.boot()
		}
		return system.Result{}, nil
	case "setpriv":
		return h.disk.Run(ctx, c)
	}
	panic("unexpected command " + c.Name)
}

func runRig(t *testing.T, cfg guestinit.Config, files map[string]string) (*rig, *hostRunner, *fakeMounter) {
	t.Helper()
	r := newRig(t, cfg)
	nft, _ := os.ReadFile("../rulehash/testdata/nft-list.json")
	h := &hostRunner{nft: nft, disk: &diskRunner{files: files}}
	r.env.Run = h
	r.env.JailUID, r.env.JailGID = os.Getuid(), os.Getgid()
	r.env.ResultUID, r.env.ResultGID = os.Getuid(), os.Getgid()
	write(t, r.env.Paths.LockPath, nil, 0o644)
	// The scope empties as soon as it starts: cgroup.events never exists.
	r.proc.files["/proc/self/mountinfo"] = ""
	return r, h, &fakeMounter{}
}

func TestRunReview(t *testing.T) {
	r, h, m := runRig(t, reviewCfg(), map[string]string{"/stream.jsonl": "{}\n", "/stderr.log": "", "/exit-code": "0\n"})
	res, err := Run(context.Background(), r.env, m, r.id, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(res.Copied, []string{"stream.jsonl", "stderr.log", "exit-code"}) {
		t.Errorf("copied %v", res.Copied)
	}
	wantOrder := []string{"nft", "mkfs.ext4", "systemd-run", "setpriv", "setpriv", "setpriv"}
	if !slices.Equal(h.names, wantOrder) {
		t.Errorf("commands %v, want %v", h.names, wantOrder)
	}
	if _, err := os.Stat(filepath.Join(r.env.Paths.JailerBase, "firecracker", r.id.String())); !os.IsNotExist(err) {
		t.Errorf("the jail stayed: %v", err)
	}
	entries, _ := os.ReadDir(r.dir)
	if len(entries) != 1 || entries[0].Name() != "result" {
		t.Errorf("run folder holds %v", entries)
	}
}

// recordingRunner notes where each command's output goes.
type recordingRunner struct {
	*hostRunner
	stdout map[string]any
}

func (r *recordingRunner) Run(ctx context.Context, c system.Cmd) (system.Result, error) {
	r.stdout[c.Name] = c.Stdout
	return r.hostRunner.Run(ctx, c)
}

func TestRunHandsTheVMTheConsole(t *testing.T) {
	r, h, m := runRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	console := os.Stderr
	rr := &recordingRunner{hostRunner: h, stdout: map[string]any{}}
	r.env.Run, r.env.Console = rr, console
	if _, err := Run(context.Background(), r.env, m, r.id, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// An *os.File passes straight to the VM, with no pipe for os/exec to
	// close after systemd-run returns.
	if got, ok := rr.stdout["systemd-run"].(*os.File); !ok || got != console {
		t.Errorf("systemd-run stdout = %v, want the console file", rr.stdout["systemd-run"])
	}
}

func TestRunRefusesWhileTheLockIsHeld(t *testing.T) {
	r, h, m := runRig(t, reviewCfg(), nil)
	l, err := step.Lock(context.Background(), r.env.Paths.LockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if _, err := Run(context.Background(), r.env, m, r.id, false); !errors.Is(err, step.ErrBusy) {
		t.Errorf("Run = %v, want ErrBusy", err)
	}
	if len(h.names) != 0 {
		t.Errorf("Run ran %v while another run held the lock", h.names)
	}
}

func TestRunStopsAtAFailedCheck(t *testing.T) {
	r, h, m := runRig(t, reviewCfg(), nil)
	write(t, r.env.Paths.RulesHash, []byte("0000\n"), 0o644)
	_, err := Run(context.Background(), r.env, m, r.id, false)
	var ce *CheckError
	if !errors.As(err, &ce) || ce.Check != CheckRules {
		t.Fatalf("Run = %v, want a rule-hash refusal", err)
	}
	if slices.Contains(h.names, "systemd-run") {
		t.Error("Run booted after a failed check")
	}
}

func TestRunKillsAndCleansUpWhenInterrupted(t *testing.T) {
	r, h, m := runRig(t, reviewCfg(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	events := filepath.Join(r.env.Paths.CgroupRoot, "system.slice", "codecheck-"+r.id.String()+".scope", "cgroup.events")
	// Running when the interruption arrives; gone once the kill lands.
	r.env.Proc = &eventsProc{fakeProc: r.proc, path: events, readings: []string{"populated 1\n", ""}}
	h.boot = cancel // the CLI's user pressed Ctrl+C mid-run
	_, err := Run(ctx, r.env, m, r.id, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the cancellation", err)
	}
	if !slices.Contains(h.names, "systemctl") {
		t.Error("Run never killed the VM's scope")
	}
	if _, err := os.Stat(filepath.Join(r.env.Paths.JailerBase, "firecracker", r.id.String())); !os.IsNotExist(err) {
		t.Errorf("the jail stayed after the interruption: %v", err)
	}
}

func TestRunBuild(t *testing.T) {
	cfg := guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"}
	r, h, m := runRig(t, cfg, map[string]string{"/etc/codecheck-sandbox/build-complete": "cc-20261121-101500-a1b2c3\n"})
	os.Remove(r.env.Paths.ImagesSum)
	if _, err := Run(context.Background(), r.env, m, r.id, true); err != nil {
		t.Fatalf("Run build: %v", err)
	}
	if slices.Contains(h.names, "nft") {
		t.Error("a build checked the network rules")
	}
	entries, _ := os.ReadDir(r.dir)
	if len(entries) != 1 || entries[0].Name() != "rootfs.ext4" {
		t.Errorf("run folder after a build holds %v, want only rootfs.ext4", entries)
	}
}
