package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

func rebuildSteps() []scriptStep {
	return []scriptStep{
		{id: "platform-kvm", phase: step.PhasePlatform, ok: true},
		{id: "host-rules", phase: step.PhaseHost, ok: true},
		{id: "guest-firecracker", phase: step.PhaseGuest, ok: true},
		{id: "guest-image", phase: step.PhaseGuest, ok: true}, // already built
	}
}

func TestRebuildForcesTheImage(t *testing.T) {
	f := &fakeSetup{steps: rebuildSteps()}
	out, err := runIn(t, setupEnv(f, passingSelftest()), "rebuild", "--yes")
	if err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	if !slices.Equal(f.applied, []string{"guest-image"}) {
		t.Errorf("applied %v, want only the image, rebuilt though its check passed", f.applied)
	}
	if f.deps.CodeCheckCommit != pins.CodeCheck.Commit {
		t.Errorf("built commit %s", f.deps.CodeCheckCommit)
	}
}

func TestRebuildWithAnotherCommit(t *testing.T) {
	commit := strings.Repeat("c", 40)
	f := &fakeSetup{steps: rebuildSteps()}
	env := setupEnv(f, passingSelftest())
	if _, err := runIn(t, env, "rebuild", "--yes", "--commit", commit); err != nil {
		t.Fatal(err)
	}
	if f.deps.CodeCheckCommit != commit {
		t.Errorf("built commit %s", f.deps.CodeCheckCommit)
	}
	if c, _, _ := env.LoadConfig(); c.CodeCheckCommit != commit {
		t.Errorf("config keeps %s after a good rebuild", c.CodeCheckCommit)
	}
}

func TestAFailedRebuildKeepsTheOldCommit(t *testing.T) {
	steps := rebuildSteps()
	steps[3].fail = errors.New("fetching CodeCheck: commit not found")
	f := &fakeSetup{steps: steps}
	env := setupEnv(f, passingSelftest())
	if _, err := runIn(t, env, "rebuild", "--yes", "--commit", strings.Repeat("d", 40)); err == nil {
		t.Fatal("a failed rebuild succeeded")
	}
	if c, _, _ := env.LoadConfig(); c.CodeCheckCommit != pins.CodeCheck.Commit {
		t.Errorf("config moved to %s after a failed rebuild", c.CodeCheckCommit)
	}
}

func TestRebuildRefusesABadCommit(t *testing.T) {
	for _, c := range []string{"main", "abc123", "v0.0.1", strings.Repeat("g", 40)} {
		f := &fakeSetup{steps: rebuildSteps()}
		if _, err := runIn(t, setupEnv(f, passingSelftest()), "rebuild", "--yes", "--commit", c); err == nil || len(f.applied) != 0 {
			t.Errorf("--commit %s: %v, applied %v", c, err, f.applied)
		}
	}
}
