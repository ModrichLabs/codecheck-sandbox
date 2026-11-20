package step

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeStep scripts a step. checks answers successive Check calls (the last
// value repeats); every call lands in log as "<id>.<method>".
type fakeStep struct {
	id       string
	root     bool
	checks   []bool
	applyErr []error // successive Apply results; nil once used up
	checkErr []error // successive Check errors, ahead of checks; nil once used up
	log      *[]string
	progress bool // Apply reports progress
	undone   []Item
	undoErr  []error // successive Undo results
}

func (s *fakeStep) ID() string    { return s.id }
func (s *fakeStep) Title() string { return "Step " + s.id }
func (s *fakeStep) Phase() Phase  { return PhaseHost }

func (s *fakeStep) Check(context.Context) (Status, error) {
	*s.log = append(*s.log, s.id+".check")
	if len(s.checkErr) > 0 {
		err := s.checkErr[0]
		s.checkErr = s.checkErr[1:]
		if err != nil {
			return Status{}, err
		}
	}
	ok := s.checks[0]
	if len(s.checks) > 1 {
		s.checks = s.checks[1:]
	}
	return Status{OK: ok, Detail: fmt.Sprintf("ok=%v", ok)}, nil
}

func (s *fakeStep) Plan(context.Context) (Plan, error) {
	*s.log = append(*s.log, s.id+".plan")
	return Plan{Cmds: []PlannedCmd{{Name: "true", Root: s.root}}, NeedsRoot: s.root}, nil
}

func (s *fakeStep) Apply(ctx context.Context) error {
	*s.log = append(*s.log, s.id+".apply")
	if s.progress {
		ReportProgress(ctx, "halfway", 0.5)
	}
	if len(s.applyErr) == 0 {
		return nil
	}
	err := s.applyErr[0]
	s.applyErr = s.applyErr[1:]
	return err
}

func (s *fakeStep) Undo(_ context.Context, items []Item) error {
	*s.log = append(*s.log, s.id+".undo")
	s.undone = append(s.undone, items...)
	if len(s.undoErr) == 0 {
		return nil
	}
	err := s.undoErr[0]
	s.undoErr = s.undoErr[1:]
	return err
}

// harness collects events and the call log for one engine run.
type harness struct {
	log      []string
	events   []Event
	confirms int
	declined bool
}

func (h *harness) step(id string, root bool, checks ...bool) *fakeStep {
	return &fakeStep{id: id, root: root, checks: checks, log: &h.log}
}

func (h *harness) run(t *testing.T, steps []Step, opts Options) error {
	t.Helper()
	ch := make(chan Event)
	done := make(chan struct{})
	go func() {
		for e := range ch {
			h.events = append(h.events, e)
		}
		close(done)
	}()
	opts.Events = ch
	if opts.Confirm == nil {
		opts.Confirm = func(context.Context, Step, Plan) (bool, error) {
			h.confirms++
			return !h.declined, nil
		}
	}
	if opts.Elevate == nil {
		opts.Elevate = func(context.Context) error {
			h.log = append(h.log, "elevate")
			return nil
		}
	}
	err := Run(context.Background(), steps, opts)
	close(ch)
	<-done
	return err
}

// kinds renders the events as "Kind:id" for compact comparison.
func (h *harness) kinds() []string {
	var out []string
	for _, e := range h.events {
		out = append(out, fmt.Sprintf("%s:%s", e.Kind, e.StepID))
	}
	return out
}

func TestRunSkipsPassingSteps(t *testing.T) {
	h := &harness{}
	err := h.run(t, []Step{h.step("a", false, true)}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"a.check"}; !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
	if len(h.events) != 2 || !h.events[1].Skipped {
		t.Errorf("events = %+v, want StepStarted then a skipped StepDone", h.events)
	}
}

func TestRunAppliesAndRechecks(t *testing.T) {
	h := &harness{}
	if err := h.run(t, []Step{h.step("a", false, false, true)}, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"a.check", "a.plan", "a.apply", "a.check"}
	if !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
	wantEvents := []string{"StepStarted:a", "StepPlanned:a", "StepNeedsConfirm:a", "StepDone:a"}
	if !reflect.DeepEqual(h.kinds(), wantEvents) {
		t.Errorf("events = %v, want %v", h.kinds(), wantEvents)
	}
	if h.confirms != 1 {
		t.Errorf("confirms = %d, want 1", h.confirms)
	}
}

func TestRunDryRunNeverApplies(t *testing.T) {
	h := &harness{}
	steps := []Step{h.step("a", true, false), h.step("b", false, true), h.step("c", false, false)}
	if err := h.run(t, steps, Options{DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, l := range h.log {
		if strings.HasSuffix(l, ".apply") || l == "elevate" {
			t.Errorf("dry run called %s", l)
		}
	}
	want := []string{"StepStarted:a", "StepPlanned:a", "StepStarted:b", "StepDone:b", "StepStarted:c", "StepPlanned:c"}
	if !reflect.DeepEqual(h.kinds(), want) {
		t.Errorf("events = %v, want %v", h.kinds(), want)
	}
	if h.confirms != 0 {
		t.Errorf("dry run asked for confirmation %d times", h.confirms)
	}
}

func TestRunDeclinedConfirmStops(t *testing.T) {
	h := &harness{declined: true}
	err := h.run(t, []Step{h.step("a", false, false), h.step("b", false, false)}, Options{})
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("err = %v, want ErrDeclined", err)
	}
	if want := []string{"a.check", "a.plan"}; !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
}

func TestRunAssumeYesSkipsConfirm(t *testing.T) {
	h := &harness{}
	if err := h.run(t, []Step{h.step("a", false, false, true)}, Options{AssumeYes: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.confirms != 0 {
		t.Errorf("confirms = %d, want 0 with AssumeYes", h.confirms)
	}
	for _, k := range h.kinds() {
		if strings.HasPrefix(k, "StepNeedsConfirm") {
			t.Errorf("AssumeYes still emitted %s", k)
		}
	}
}

func TestRunFailedRecheckFailsTheStep(t *testing.T) {
	h := &harness{}
	err := h.run(t, []Step{h.step("a", false, false, false), h.step("b", false, false)}, Options{})
	if !errors.Is(err, ErrStillFailing) {
		t.Fatalf("err = %v, want ErrStillFailing", err)
	}
	last := h.events[len(h.events)-1]
	if last.Kind != StepFailed || last.StepID != "a" || !errors.Is(last.Err, ErrStillFailing) {
		t.Errorf("last event = %+v, want StepFailed for a", last)
	}
	for _, l := range h.log {
		if strings.HasPrefix(l, "b.") {
			t.Errorf("step b ran after a failed: %v", h.log)
		}
	}
}

func TestRunApplyErrorFailsTheStep(t *testing.T) {
	h := &harness{}
	boom := errors.New("boom")
	s := h.step("a", false, false)
	s.applyErr = []error{boom}
	if err := h.run(t, []Step{s}, Options{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if last := h.events[len(h.events)-1]; last.Kind != StepFailed || !errors.Is(last.Err, boom) {
		t.Errorf("last event = %+v, want StepFailed with boom", last)
	}
}

func TestRunElevatesOnceBeforeTheFirstRootApply(t *testing.T) {
	h := &harness{}
	steps := []Step{
		h.step("user", false, false, true),
		h.step("skip", true, true),
		h.step("r1", true, false, true),
		h.step("r2", true, false, true),
	}
	if err := h.run(t, steps, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"user.check", "user.plan", "user.apply", "user.check",
		"skip.check",
		"r1.check", "r1.plan", "elevate", "r1.apply", "r1.check",
		"r2.check", "r2.plan", "r2.apply", "r2.check",
	}
	if !reflect.DeepEqual(h.log, want) {
		t.Errorf("log =\n%v\nwant\n%v", h.log, want)
	}
	var sudo []string
	for _, k := range h.kinds() {
		if strings.HasPrefix(k, "NeedsSudo") {
			sudo = append(sudo, k)
		}
	}
	if !reflect.DeepEqual(sudo, []string{"NeedsSudo:r1"}) {
		t.Errorf("NeedsSudo events = %v, want [NeedsSudo:r1]", sudo)
	}
}

func TestRunReElevatesWhenTheLoginExpires(t *testing.T) {
	h := &harness{}
	s := h.step("r", true, false, true)
	s.applyErr = []error{fmt.Errorf("install: %w", system.ErrLoginExpired)}
	if err := h.run(t, []Step{s}, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"r.check", "r.plan", "elevate", "r.apply", "elevate", "r.apply", "r.check"}
	if !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
}

func TestRunReElevatesWhenTheLoginExpiresDuringCheck(t *testing.T) {
	h := &harness{}
	s := h.step("r", true, false, true)
	// The first check passes through; the re-check after Apply finds the
	// login gone, so the engine hands over and checks once more.
	s.checkErr = []error{nil, fmt.Errorf("stat: %w", system.ErrLoginExpired)}
	if err := h.run(t, []Step{s}, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"r.check", "r.plan", "elevate", "r.apply", "r.check", "elevate", "r.check"}
	if !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
}

func TestRunElevateFailureStops(t *testing.T) {
	h := &harness{}
	nope := errors.New("wrong password")
	err := h.run(t, []Step{h.step("r", true, false)}, Options{
		Elevate: func(context.Context) error { return nope },
	})
	if !errors.Is(err, nope) {
		t.Fatalf("err = %v, want the elevate error", err)
	}
	for _, l := range h.log {
		if l == "r.apply" {
			t.Error("Apply ran without privilege")
		}
	}
}

func TestRunForwardsProgress(t *testing.T) {
	h := &harness{}
	s := h.step("a", false, false, true)
	s.progress = true
	if err := h.run(t, []Step{s}, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"StepStarted:a", "StepPlanned:a", "StepNeedsConfirm:a", "Progress:a", "StepDone:a"}
	if !reflect.DeepEqual(h.kinds(), want) {
		t.Fatalf("events = %v, want %v", h.kinds(), want)
	}
	p := h.events[3]
	if p.Message != "halfway" || p.Fraction != 0.5 {
		t.Errorf("progress = %q %v, want halfway 0.5", p.Message, p.Fraction)
	}
}

func TestReportProgressOutsideTheEngineDoesNothing(t *testing.T) {
	ReportProgress(context.Background(), "nobody listens", 1) // must not panic
}

func TestRunWithoutEventsChannel(t *testing.T) {
	var log []string
	s := &fakeStep{id: "a", checks: []bool{false, true}, log: &log}
	err := Run(context.Background(), []Step{s}, Options{AssumeYes: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}
