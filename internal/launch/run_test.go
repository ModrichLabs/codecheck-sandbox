package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// hostRunner plays every command a run makes: nft, mkfs.ext4, systemd-run,
// systemctl and debugfs (through setpriv).
type hostRunner struct {
	mu       sync.Mutex
	nft      []byte
	disk     *diskRunner
	boot     func() // runs when systemd-run starts the VM
	bootCode int    // systemd-run's exit code
	killErr  error  // what systemctl kill returns
	killed   bool
	names    []string
}

func (h *hostRunner) wasKilled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killed
}

func (h *hostRunner) Run(ctx context.Context, c system.Cmd) (system.Result, error) {
	h.mu.Lock()
	h.names = append(h.names, c.Name)
	h.mu.Unlock()
	switch c.Name {
	case "nft":
		return system.Result{Stdout: h.nft}, nil
	case "mkfs.ext4":
		return system.Result{}, nil
	case "systemctl":
		h.mu.Lock()
		h.killed = h.killErr == nil
		h.mu.Unlock()
		return system.Result{}, h.killErr
	case "systemd-run":
		if h.boot != nil {
			h.boot()
		}
		return system.Result{Code: h.bootCode}, nil
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

// runningUntilKilled reports the scope populated until systemctl kill lands.
type runningUntilKilled struct {
	*fakeProc
	path string
	h    *hostRunner
}

func (p *runningUntilKilled) ReadFile(path string) ([]byte, error) {
	if path != p.path {
		return p.fakeProc.ReadFile(path)
	}
	if p.h.wasKilled() {
		return nil, os.ErrNotExist
	}
	return []byte("populated 1\n"), nil
}

func scopeEvents(r *rig) string {
	return filepath.Join(r.env.Paths.CgroupRoot, "system.slice", "codecheck-"+r.id.String()+".scope", "cgroup.events")
}

func TestRunLeavesTheJailWhenAFailedBootCantBeStopped(t *testing.T) {
	r, h, m := runRig(t, reviewCfg(), nil)
	h.bootCode = 1
	h.killErr = errors.New("Failed to kill unit")
	_, err := Run(context.Background(), r.env, m, r.id, false)
	var be *BootError
	if !errors.As(err, &be) {
		t.Fatalf("Run = %v, want a BootError", err)
	}
	if _, serr := os.Stat(filepath.Join(r.env.Paths.JailerBase, "firecracker", r.id.String())); serr != nil {
		t.Errorf("Run removed the jail under a VM it couldn't stop: %v", serr)
	}
}

func TestRunBuildTimeoutReportsTheTimeout(t *testing.T) {
	cfg := guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"}
	r, h, m := runRig(t, cfg, nil) // no marker: the build never finished
	os.Remove(r.env.Paths.ImagesSum)
	r.env.Proc = &runningUntilKilled{fakeProc: r.proc, path: scopeEvents(r), h: h}
	defer func(s vmShape) { shapes[guestinit.ModeBuild] = s }(shapes[guestinit.ModeBuild])
	s := shapes[guestinit.ModeBuild]
	s.limit = 20 * time.Millisecond
	shapes[guestinit.ModeBuild] = s

	_, err := Run(context.Background(), r.env, m, r.id, true)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("Run = %v, want ErrTimeout among the errors", err)
	}
}

func TestScopeBackstopOutlastsTheWait(t *testing.T) {
	// systemd's own limit must fire after Wait's kill, or a timed-out run
	// looks like one that finished.
	for mode, s := range shapes {
		id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
		argv := (&Jail{Unit: "codecheck-" + id.String() + ".scope"}).Argv(Env{Paths: DefaultPaths()}, Job{ID: id, Config: guestinit.Config{Mode: mode}})
		var max int
		for _, a := range argv {
			if v, ok := strings.CutPrefix(a, "RuntimeMaxSec="); ok {
				max, _ = strconv.Atoi(v)
			}
		}
		if time.Duration(max)*time.Second <= s.limit {
			t.Errorf("%s: RuntimeMaxSec=%d, want more than Wait's %v", mode, max, s.limit)
		}
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
