package step

import (
	"context"
	"errors"
	"fmt"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// EventKind names what an Event reports.
type EventKind string

const (
	StepStarted      EventKind = "StepStarted"
	StepPlanned      EventKind = "StepPlanned"
	StepNeedsConfirm EventKind = "StepNeedsConfirm"
	StepDone         EventKind = "StepDone"
	StepFailed       EventKind = "StepFailed"
	NeedsSudo        EventKind = "NeedsSudo"
	Progress         EventKind = "Progress"
)

// Event tells the interface what the engine does. Only the fields that suit
// Kind get set.
type Event struct {
	Kind   EventKind
	StepID string
	Title  string
	Plan   *Plan  // StepPlanned
	Status Status // StepDone
	// Skipped marks a StepDone whose Check passed before any work.
	Skipped  bool
	Err      error   // StepFailed
	Message  string  // Progress
	Fraction float64 // Progress, from 0 to 1
}

// ConfirmFunc asks the user to approve a step's plan.
type ConfirmFunc func(ctx context.Context, s Step, p Plan) (bool, error)

// ElevateFunc runs the interactive privilege login (the sudo -v handover).
type ElevateFunc func(ctx context.Context) error

// Options control one engine run.
type Options struct {
	// DryRun checks and plans every step but applies none.
	DryRun bool
	// AssumeYes applies plans without asking (--yes).
	AssumeYes bool
	// Events, when set, receives every Event. Run blocks on each send, so the
	// receiver must keep reading until Run returns.
	Events chan<- Event
	// Confirm approves each plan unless AssumeYes holds; nil approves all.
	Confirm ConfirmFunc
	// Elevate runs before the first root step that applies, and again when
	// the login expires mid-step; nil skips the handover.
	Elevate ElevateFunc
}

var (
	// ErrDeclined reports a plan the user did not approve.
	ErrDeclined = errors.New("declined")
	// ErrStillFailing reports a step whose Check still fails after Apply.
	ErrStillFailing = errors.New("check still fails after apply")
)

// Run takes each step in order: a passing Check skips it; otherwise the
// engine plans, confirms, elevates when the plan needs root, applies and
// checks again. The first failure or refusal stops the run, since later steps
// build on earlier ones.
func Run(ctx context.Context, steps []Step, opts Options) error {
	e := &engine{opts: opts}
	for _, s := range steps {
		if err := e.runStep(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s.ID(), err)
		}
	}
	return nil
}

type engine struct {
	opts     Options
	elevated bool
}

func (e *engine) emit(ctx context.Context, ev Event) {
	if e.opts.Events == nil {
		return
	}
	select {
	case e.opts.Events <- ev:
	case <-ctx.Done():
	}
}

func (e *engine) runStep(ctx context.Context, s Step) error {
	base := Event{StepID: s.ID(), Title: s.Title()}
	at := func(k EventKind) Event { return with(base, k) }
	fail := func(err error) error {
		ev := at(StepFailed)
		ev.Err = err
		e.emit(ctx, ev)
		return err
	}

	check := func() (Status, error) {
		var st Status
		err := e.withLogin(ctx, base, func() (err error) {
			st, err = s.Check(ctx)
			return err
		})
		return st, err
	}

	e.emit(ctx, at(StepStarted))
	st, err := check()
	if err != nil {
		return fail(err)
	}
	if st.OK {
		ev := at(StepDone)
		ev.Status, ev.Skipped = st, true
		e.emit(ctx, ev)
		return nil
	}

	plan, err := s.Plan(ctx)
	if err != nil {
		return fail(err)
	}
	ev := at(StepPlanned)
	ev.Plan = &plan
	e.emit(ctx, ev)
	if e.opts.DryRun {
		return nil
	}

	if !e.opts.AssumeYes && e.opts.Confirm != nil {
		e.emit(ctx, at(StepNeedsConfirm))
		ok, err := e.opts.Confirm(ctx, s, plan)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return fail(ErrDeclined)
		}
	}

	if plan.NeedsRoot && !e.elevated {
		if err := e.elevate(ctx, base); err != nil {
			return fail(err)
		}
	}
	err = e.withLogin(ctx, base, func() error { return s.Apply(withProgress(ctx, e, base)) })
	if err != nil {
		return fail(err)
	}

	st, err = check()
	if err != nil {
		return fail(err)
	}
	if !st.OK {
		return fail(fmt.Errorf("%w: %s", ErrStillFailing, st.Detail))
	}
	done := at(StepDone)
	done.Status = st
	e.emit(ctx, done)
	return nil
}

// withLogin runs fn and, when it fails because the privilege login lapsed
// despite the keep-alive, hands over again and retries once. Steps check
// before they act, so a retry stays safe.
func (e *engine) withLogin(ctx context.Context, base Event, fn func() error) error {
	err := fn()
	if !errors.Is(err, system.ErrLoginExpired) {
		return err
	}
	if err := e.elevate(ctx, base); err != nil {
		return err
	}
	return fn()
}

func (e *engine) elevate(ctx context.Context, base Event) error {
	if e.opts.Elevate == nil {
		e.elevated = true
		return nil
	}
	ev := base
	ev.Kind = NeedsSudo
	e.emit(ctx, ev)
	if err := e.opts.Elevate(ctx); err != nil {
		return err
	}
	e.elevated = true
	return nil
}

type progressKey struct{}

type progressSink struct {
	e    *engine
	base Event
}

func withProgress(ctx context.Context, e *engine, base Event) context.Context {
	return context.WithValue(ctx, progressKey{}, progressSink{e: e, base: base})
}

// ReportProgress lets a long Apply (the guest build, say) report how far it
// has got. Outside an engine run it does nothing.
func ReportProgress(ctx context.Context, message string, fraction float64) {
	sink, ok := ctx.Value(progressKey{}).(progressSink)
	if !ok {
		return
	}
	ev := sink.base
	ev.Kind, ev.Message, ev.Fraction = Progress, message, fraction
	sink.e.emit(ctx, ev)
}
