package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// recorder collects captured events.
type recorder struct {
	events  []Event
	flushes int
}

func (r *recorder) Capture(e Event)       { r.events = append(r.events, e) }
func (r *recorder) Flush(context.Context) { r.flushes++ }

func TestStepRecorderSendsStepFinished(t *testing.T) {
	RegisterStepIDs("jail-user", "nft-rules")
	r := &recorder{}
	rec := StepRecorder(r)
	rec(step.StepResult{ID: "jail-user", Outcome: step.OutcomeOK, Duration: 2 * time.Second})
	rec(step.StepResult{ID: "nft-rules", Outcome: step.OutcomeFailed, Duration: time.Minute, Err: errors.New("nft: /etc/x")})

	if len(r.events) != 3 {
		t.Fatalf("events = %+v, want step_finished, step_finished, $exception", r.events)
	}
	ok := r.events[0].(StepFinished)
	if ok.Step != "jail-user" || ok.Outcome != OutcomeOK || ok.Duration != "<10s" {
		t.Errorf("first = %+v", ok)
	}
	failed := r.events[1].(StepFinished)
	if failed.Outcome != OutcomeFailed || failed.Duration != "1-5m" {
		t.Errorf("second = %+v", failed)
	}
	ex := r.events[2].(Exception)
	if ex.Step != "nft-rules" || ex.Kind != "unknown" {
		t.Errorf("exception = %+v", ex)
	}
}

func TestStepRecorderSkipsExceptionsForDeclinedSteps(t *testing.T) {
	r := &recorder{}
	StepRecorder(r)(step.StepResult{ID: "x", Outcome: step.OutcomeDeclined, Err: step.ErrDeclined})
	if len(r.events) != 1 {
		t.Errorf("events = %+v, want only step_finished: declining is not an error", r.events)
	}
}

// TestHelpersNeverImportTelemetry guards spec §11: cc-launch (and any other
// cc-* helper) makes no network calls, so it may not depend on this package,
// on posthog-go or on net/http.
func TestHelpersNeverImportTelemetry(t *testing.T) {
	ctx := context.Background()
	run := system.ExecRunner{}
	root, _ := filepath.Abs("../..")
	list, err := run.Run(ctx, system.Cmd{Name: "go", Args: []string{"list", "-C", root, "./cmd/..."}})
	if err != nil || list.Code != 0 {
		t.Fatalf("go list: %v %s", err, list.Stderr)
	}
	forbidden := []string{
		"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry",
		"github.com/posthog/posthog-go",
		"net/http",
	}
	helpers := 0
	for _, pkg := range strings.Fields(string(list.Stdout)) {
		if !strings.Contains(pkg, "/cmd/cc-") {
			continue
		}
		helpers++
		deps, err := run.Run(ctx, system.Cmd{Name: "go", Args: []string{"list", "-C", root, "-deps", pkg}})
		if err != nil || deps.Code != 0 {
			t.Fatalf("go list -deps %s: %v %s", pkg, err, deps.Stderr)
		}
		for _, d := range strings.Fields(string(deps.Stdout)) {
			for _, f := range forbidden {
				if d == f {
					t.Errorf("%s depends on %s", pkg, f)
				}
			}
		}
	}
	t.Logf("checked %d cc-* helpers", helpers)
}
