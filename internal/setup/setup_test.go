package setup

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/distro"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/platform"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// recorder answers every command with a failure, as on a bare host, and
// records each call.
type recorder struct {
	mu    sync.Mutex
	calls []system.Cmd
}

func (r *recorder) Run(_ context.Context, c system.Cmd) (system.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	return system.Result{Code: 1}, nil
}

type nopLauncher struct{}

func (nopLauncher) Build(context.Context, launch.RunID) error {
	return errors.New("no builds in tests")
}
func (nopLauncher) Clean(context.Context, launch.RunID) error { return nil }

// bareHost builds Deps for an x86_64 Arch host with nothing installed.
func bareHost(t *testing.T) (Deps, *recorder) {
	t.Helper()
	r := &recorder{}
	fsys := system.NewMemFS()
	fsys.MkdirAll("/usr/bin", 0o755)
	fsys.WriteFile("/usr/bin/codecheck-sandbox", []byte("\x7fELF cli"), 0o755)
	fsys.WriteFile("/usr/bin/cc-launch", []byte("\x7fELF launch"), 0o755)
	notFound := func(string) (string, error) { return "", errors.New("not found") }
	return Deps{
		Platform: platform.Env{
			FS: fsys, Runner: r, Arch: "amd64",
			Stat:        func(string) (platform.FileStat, error) { return platform.FileStat{}, fs.ErrNotExist },
			FreeBytes:   func(string) (uint64, error) { return 100 << 30, nil },
			LookPath:    notFound,
			LookupGroup: func(uint32) (string, error) { return "", errors.New("no group") },
		},
		Family:     distro.Arch,
		Root:       r,
		User:       "alice",
		FS:         fsys,
		Look:       notFound,
		GuestInit:  "/opt/codecheck-sandbox/cc-guestinit",
		Cache:      t.TempDir(),
		Launcher:   nopLauncher{},
		Now:        time.Now,
		Executable: func() (string, error) { return "/usr/bin/codecheck-sandbox", nil },
	}, r
}

func TestStepsRunInPhaseOrder(t *testing.T) {
	d, _ := bareHost(t)
	steps, err := Steps(d)
	if err != nil {
		t.Fatalf("Steps: %v", err)
	}
	order := []step.Phase{step.PhasePlatform, step.PhaseComponents, step.PhaseHost, step.PhaseGuest}
	last := 0
	ids := map[string]bool{}
	for _, s := range steps {
		i := slices.Index(order, s.Phase())
		if i < last {
			t.Errorf("%s (%s) comes after a later phase", s.ID(), s.Phase())
		}
		last = i
		if ids[s.ID()] {
			t.Errorf("step id %s repeats", s.ID())
		}
		ids[s.ID()] = true
	}
	for _, want := range []string{"platform-kvm", "components", "host-jail-user", "host-proxy-service", "host-sudoers", "guest-firecracker", "guest-image"} {
		if !ids[want] {
			t.Errorf("missing step %s", want)
		}
	}
}

// A dry run on a bare host checks and plans every step it reaches and
// changes nothing: every command it runs reads.
func TestDryRunChangesNothing(t *testing.T) {
	d, r := bareHost(t)
	all, err := Steps(d)
	if err != nil {
		t.Fatal(err)
	}
	// The platform checks only read, and the first failing one stops the
	// run, so start after them to reach every step that would change things.
	var steps []step.Step
	for _, s := range all {
		if s.Phase() != step.PhasePlatform {
			steps = append(steps, s)
		}
	}
	var planned []step.Plan
	events := make(chan step.Event)
	done := make(chan error, 1)
	go func() {
		done <- step.Run(context.Background(), steps, step.Options{DryRun: true, Events: events})
		close(events)
	}()
	skipped := 0
	for ev := range events {
		if ev.Kind == step.StepPlanned && ev.Plan != nil {
			planned = append(planned, *ev.Plan)
		}
		if ev.Kind == step.StepDone && ev.Skipped {
			skipped++ // nothing to do here, such as ufw on a host without it
		}
	}
	if err := <-done; err != nil {
		t.Errorf("the dry run stopped: %v", err)
	}
	// The run reaches every step: planned, or passed as not needed here.
	if len(planned)+skipped != len(steps) || len(planned) < len(steps)/2 {
		t.Errorf("planned %d and skipped %d of %d steps", len(planned), skipped, len(steps))
	}
	for _, c := range r.calls {
		if writes(c) {
			t.Errorf("the dry run ran %s %v", c.Name, c.Args)
		}
	}
}

// writes spots commands that change the host.
func writes(c system.Cmd) bool {
	argv := strings.Join(append([]string{c.Name}, c.Args...), " ")
	for _, w := range []string{"install ", "dd ", "useradd", "systemctl enable", "systemctl start", "systemctl restart",
		"nft -f", "rm ", "mv ", "mkdir", "chown", "chmod", "pacman -S", "apt-get", "dnf install", "zypper", "visudo -cf"} {
		if strings.HasPrefix(argv, w) || strings.Contains(argv, " "+w) {
			return !strings.HasPrefix(argv, "systemctl is-")
		}
	}
	return false
}

func TestInfoMirrorsTheSteps(t *testing.T) {
	d, _ := bareHost(t)
	steps, _ := Steps(d)
	info := Info(steps)
	if len(info) != len(steps) || info[0].ID != steps[0].ID() || info[len(info)-1].Phase != step.PhaseGuest {
		t.Errorf("info = %+v", info)
	}
	sb := Sidebar()
	if sb[0].Title != "Welcome" || sb[len(sb)-1].Title != "Summary" {
		t.Errorf("sidebar = %+v", sb)
	}
}
