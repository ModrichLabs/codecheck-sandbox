package guest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

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

func TestFootprintMatchesWhatTheStepsUndo(t *testing.T) {
	paths := launch.DefaultPaths()
	items := Footprint(paths)
	r := &okRunner{}
	steps := Steps(Deps{Paths: paths, Root: r})
	byID := map[string]step.Step{}
	for _, s := range steps {
		byID[s.ID()] = s
	}
	perStep := map[string][]step.Item{}
	for _, it := range items {
		if err := step.CheckPath(it.Path); err != nil || it.State != step.Created {
			t.Errorf("%+v: %v", it, err)
		}
		if byID[it.ID] == nil {
			t.Errorf("%+v: no guest step %s", it, it.ID)
			continue
		}
		perStep[it.ID] = append(perStep[it.ID], it)
	}
	for _, want := range []string{paths.Firecracker, paths.Jailer, paths.Kernel, paths.RootDisk, paths.ImagesSum, StampPath(paths)} {
		found := false
		for _, it := range items {
			found = found || it.Path == want
		}
		if !found {
			t.Errorf("the footprint misses %s", want)
		}
	}
	for id, its := range perStep {
		r.calls = nil
		if err := byID[id].Undo(context.Background(), its); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		for _, it := range its {
			found := false
			for _, c := range r.calls {
				found = found || strings.Contains(c, it.Path)
			}
			if !found {
				t.Errorf("%s's Undo ignored %s (ran %v)", id, it.Path, r.calls)
			}
		}
	}
}
