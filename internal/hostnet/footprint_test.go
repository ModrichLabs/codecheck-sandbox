package hostnet

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// okRunner answers every command with success and records it.
type okRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *okRunner) Run(_ context.Context, c system.Cmd) (system.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.Join(append([]string{c.Name}, c.Args...), " "))
	return system.Result{}, nil
}

// Every footprint item must pass the manifest's own checks, belong to a host
// step, and get acted on by that step's Undo.
func TestFootprintMatchesWhatTheStepsUndo(t *testing.T) {
	cfg := DefaultConfig()
	items := Footprint(cfg)
	if len(items) < 15 {
		t.Fatalf("footprint holds %d items", len(items))
	}
	r := &okRunner{}
	deps := newRig().deps()
	deps.Runner = r
	steps, err := Steps(deps)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]step.Step{}
	for _, s := range steps {
		byID[s.ID()] = s
	}
	perStep := map[string][]step.Item{}
	for _, it := range items {
		if it.State != step.Created {
			t.Errorf("%+v: footprint items count as created", it)
		}
		switch it.Kind {
		case step.KindFile, step.KindDir:
			if err := step.CheckPath(it.Path); err != nil {
				t.Errorf("%+v: %v", it, err)
			}
		default:
			if err := step.CheckName(it.Kind, it.Name); err != nil {
				t.Errorf("%+v: %v", it, err)
			}
		}
		if _, ok := byID[it.ID]; !ok {
			t.Errorf("%+v: no host step %s", it, it.ID)
			continue
		}
		perStep[it.ID] = append(perStep[it.ID], it)
	}
	// Uninstall hands each step all its items at once.
	for id, its := range perStep {
		r.calls = nil
		if err := byID[id].Undo(context.Background(), its); err != nil {
			t.Errorf("%s undoing %v: %v", id, its, err)
		}
		for _, it := range its {
			target := it.Path
			if target == "" {
				target = it.Name
			}
			if it.Kind == step.KindNetwork {
				target = strings.Fields(strings.TrimPrefix(target, "inet "))[0]
			}
			if it.Kind == step.KindNetwork && it.Name == cfg.Tap {
				// The tap goes with its unit: stopping codecheck-net.service
				// deletes the device.
				target = "disable --now " + filepath.Base(cfg.UnitPath)
			}
			found := false
			for _, c := range r.calls {
				if strings.Contains(c, target) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s's Undo ignored %+v (ran %v)", id, it, r.calls)
			}
		}
	}
}
