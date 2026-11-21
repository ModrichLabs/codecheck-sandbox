package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

func TestAppRoutesEngineEventsToSteps(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		emit(ctx, opts,
			ev(step.StepStarted, "platform-kvm"),
			step.Event{Kind: step.StepDone, StepID: "platform-kvm", Skipped: true, Status: step.Status{OK: true}},
			ev(step.StepStarted, "components"),
			ev(step.StepDone, "components"),
			ev(step.StepStarted, "host-user"),
		)
		<-ctx.Done()
		return ctx.Err()
	})))
	h.until("host-user to run", func(a *App) bool { return a.StepState("host-user") == theme.Running })
	if a := h.app(); a.StepState("platform-kvm") != theme.Skipped || a.StepState("components") != theme.Done {
		t.Errorf("states: kvm %v, components %v", a.StepState("platform-kvm"), a.StepState("components"))
	}
	if got := h.app().StepState("host-tap"); got != theme.Pending {
		t.Errorf("a step not yet started shows %v", got)
	}
}

func TestAppFinishesWhenTheRunEnds(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		emit(ctx, opts, ev(step.StepStarted, "platform-kvm"), ev(step.StepDone, "platform-kvm"))
		return nil
	})))
	h.until("the run to finish", func(a *App) bool { return a.Finished() })
	if err := h.app().Err(); err != nil {
		t.Errorf("Err = %v", err)
	}
	h.press("enter")
	h.untilQuit()
}

func TestAppKeepsTheRunsError(t *testing.T) {
	boom := errors.New("boom")
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		failed := ev(step.StepFailed, "host-user")
		failed.Err = boom
		emit(ctx, opts, ev(step.StepStarted, "host-user"), failed)
		return boom
	})))
	h.until("the run to fail", func(a *App) bool { return a.Finished() })
	if !errors.Is(h.app().Err(), boom) || h.app().StepState("host-user") != theme.Failed {
		t.Errorf("Err = %v, state %v", h.app().Err(), h.app().StepState("host-user"))
	}
}

func TestQuitWhileIdleQuitsAtOnce(t *testing.T) {
	cancelled := make(chan struct{})
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		emit(ctx, opts, ev(step.StepStarted, "platform-kvm"))
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})))
	h.until("the first step", func(a *App) bool { return a.StepState("platform-kvm") == theme.Running })
	h.press("q")
	h.untilQuit()
	<-cancelled // quitting stops the engine
	if !errors.Is(h.app().Err(), ErrQuit) {
		t.Errorf("Err = %v, want ErrQuit", h.app().Err())
	}
}

func TestQuitDuringARootStepAsksFirst(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
				planned := ev(step.StepPlanned, "host-tap")
				planned.Plan = &step.Plan{NeedsRoot: true}
				emit(ctx, opts, ev(step.StepStarted, "host-tap"), planned)
				<-ctx.Done()
				return ctx.Err()
			})))
			h.until("the root step", func(a *App) bool { return a.rootBusy() })
			h.press(key)
			if h.quit || !h.app().confirmingQuit {
				t.Fatal("quit during a root step without asking")
			}
			h.press("n")
			if h.app().confirmingQuit || h.quit {
				t.Fatal("n didn't take the question away")
			}
			h.press(key)
			h.press("y")
			h.untilQuit()
		})
	}
}
