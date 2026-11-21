package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// hostSoFar takes the run up to the review tap in the host phase.
func hostSoFar(ctx context.Context, opts step.Options) {
	done := func(id string) step.Event {
		e := ev(step.StepDone, id)
		e.Status.OK = true
		return e
	}
	emit(ctx, opts,
		ev(step.StepStarted, "platform-kvm"), done("platform-kvm"),
		ev(step.StepStarted, "components"), done("components"),
		ev(step.StepStarted, "host-user"), done("host-user"),
		ev(step.StepStarted, "host-tap"),
	)
}

var tapPlan = step.Plan{
	NeedsRoot: true,
	Cmds: []step.PlannedCmd{
		{Name: "ip", Args: []string{"tuntap", "add", "dev", "cc-tap0", "mode", "tap", "user", "fcjail"}, Root: true},
		{Name: "ip", Args: []string{"addr", "add", "172.16.0.1/30", "dev", "cc-tap0"}, Root: true},
	},
	Files: []step.PlannedFile{{
		Path: "/etc/codecheck-sandbox/rules.nft", Mode: 0o644,
		Content: []byte("destroy table inet codecheck\ntable inet codecheck {\n  chain input { type filter hook input priority 0; }\n}\n"),
	}},
}

// snapshot fixes the spinner on its first frame, then returns the screen.
func snapshot(h *harness) string {
	h.app().spin = newSpinner(h.app().th)
	return h.screen()
}

func TestRunningScreen(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		hostSoFar(ctx, opts)
		<-ctx.Done()
		return ctx.Err()
	})))
	h.until("the tap step", func(a *App) bool { return a.StepState("host-tap") == theme.Running })
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "running", s)
}

func TestSidebarStates(t *testing.T) {
	a := New(testConfig(nil))
	a.states = map[string]theme.State{
		"platform-kvm": theme.Skipped, "components": theme.Done,
		"host-user": theme.Done, "host-tap": theme.Running,
	}
	a.current = "host-tap"
	want := []theme.State{theme.Done, theme.Done, theme.Done, theme.Done, theme.Current, theme.Pending, theme.Pending, theme.Pending}
	for i, e := range a.cfg.Sidebar {
		if got := a.entryState(i); got != want[i] {
			t.Errorf("%s: %v, want %v", e.Title, got, want[i])
		}
	}
	a.states["host-tap"] = theme.Failed
	if got := a.entryState(4); got != theme.Failed {
		t.Errorf("host network with a failed step: %v", got)
	}
	a.states = map[string]theme.State{"platform-kvm": theme.Running}
	a.current = "platform-kvm"
	if got := a.entryState(0); got != theme.Done {
		t.Errorf("welcome before the first phase: %v", got)
	}
}

// confirmRun asks to confirm the tap plan and reports the answer.
func confirmRun(answer chan<- bool) RunFunc {
	return func(ctx context.Context, opts step.Options) error {
		hostSoFar(ctx, opts)
		ok, err := opts.Confirm(ctx, fakeStep{id: "host-tap", title: "Review tap cc-tap0", phase: step.PhaseHost}, tapPlan)
		if err != nil {
			return err
		}
		answer <- ok
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestConfirmScreen(t *testing.T) {
	answer := make(chan bool, 1)
	h := start(t, New(testConfig(confirmRun(answer))))
	h.until("the confirm panel", func(a *App) bool { return a.confirm != nil })
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "confirm", s)
	for _, want := range []string{"Needs root", "ip tuntap add dev cc-tap0 mode tap user fcjail", "write /etc/codecheck-sandbox/rules.nft"} {
		if !strings.Contains(s, want) {
			t.Errorf("confirm panel lacks %q", want)
		}
	}
	h.press("enter")
	if ok := <-answer; !ok {
		t.Error("Enter didn't approve the plan")
	}
}

func TestConfirmSkipDeclines(t *testing.T) {
	answer := make(chan bool, 1)
	h := start(t, New(testConfig(confirmRun(answer))))
	h.until("the confirm panel", func(a *App) bool { return a.confirm != nil })
	h.press("s")
	if ok := <-answer; ok {
		t.Error("s approved the plan")
	}
	if h.app().confirm != nil {
		t.Error("the confirm panel stayed up")
	}
}

func TestConfirmShowsFiles(t *testing.T) {
	answer := make(chan bool, 1)
	h := start(t, New(testConfig(confirmRun(answer))))
	h.until("the confirm panel", func(a *App) bool { return a.confirm != nil })
	h.press("d")
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "confirm-files", s)
	if !strings.Contains(s, "destroy table inet codecheck") {
		t.Error("d didn't show the file's contents")
	}
	h.press("enter") // Enter inside the file view must not approve
	select {
	case <-answer:
		t.Fatal("Enter approved the plan from the file view")
	default:
	}
	h.press("d")
	if strings.Contains(h.screen(), "destroy table") {
		t.Error("d didn't close the file view")
	}
	h.press("enter")
	if ok := <-answer; !ok {
		t.Error("Enter didn't approve after closing the file view")
	}
}

func TestProgressScreen(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		p := ev(step.Progress, "guest-image")
		p.Message, p.Fraction = "Building the guest root disk", 0.42
		emit(ctx, opts, ev(step.StepStarted, "guest-image"), p)
		<-ctx.Done()
		return ctx.Err()
	})))
	h.until("progress", func(a *App) bool { return a.progress.msg != "" })
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "progress", s)
	if !strings.Contains(s, "42%") || !strings.Contains(s, "Building the guest root disk") {
		t.Error("progress screen lacks the fraction or message")
	}
}

func TestFailureScreenAndRetry(t *testing.T) {
	var runs atomic.Int32
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		emit(ctx, opts, ev(step.StepStarted, "host-tap"))
		if runs.Add(1) == 1 {
			err := fmt.Errorf("host-tap: %w", system.ErrLoginExpired)
			failed := ev(step.StepFailed, "host-tap")
			failed.Err = err
			emit(ctx, opts, failed)
			return err
		}
		emit(ctx, opts, ev(step.StepDone, "host-tap"))
		return nil
	})))
	h.until("the failure", func(a *App) bool { return a.Finished() })
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "failure", s)
	if !strings.Contains(s, "privilege login expired") || !strings.Contains(s, "Fix:") {
		t.Errorf("failure screen lacks the cause or fix:\n%s", s)
	}
	h.press("r")
	h.until("the retry to finish", func(a *App) bool { return a.Finished() && a.Err() == nil })
	if runs.Load() != 2 || h.app().StepState("host-tap") != theme.Done {
		t.Errorf("runs %d, state %v", runs.Load(), h.app().StepState("host-tap"))
	}
}

// Error text can quote a command's stderr; its escapes mustn't reach the
// terminal.
func TestFailureScreenStripsEscapes(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		return errors.New("nft: \x1b]0;pwned\x07 syntax error")
	})))
	h.until("the failure", func(a *App) bool { return a.Finished() })
	if raw := h.m.View().Content; strings.Contains(raw, "\x1b]0;") || !strings.Contains(stripANSI(raw), "syntax error") {
		t.Errorf("failure view: %q", stripANSI(raw))
	}
}

func TestPlanDetailsStripEscapes(t *testing.T) {
	p := step.Plan{Files: []step.PlannedFile{{Path: "/etc/x", Mode: 0o644, Content: []byte("a\x1b]52;c;eA==\x07b\n")}}}
	// The preview must show every byte that goes on disk, so control
	// characters appear spelt out rather than vanish or act.
	if got := planDetails(p); strings.Contains(got, "\x1b") || !strings.Contains(got, `a\x1b]52;c;eA==\x07b`) {
		t.Errorf("planDetails = %q", got)
	}
	cmd := system.Cmd{Name: "printf", Args: []string{"\x1b[2J"}}
	if got := commandLine(cmd); got != `printf '\x1b[2J'` {
		t.Errorf("commandLine = %q", got)
	}
}

func TestQuitQuestionScreen(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		hostSoFar(ctx, opts)
		planned := ev(step.StepPlanned, "host-tap")
		planned.Plan = &tapPlan
		emit(ctx, opts, planned)
		<-ctx.Done()
		return ctx.Err()
	})))
	h.until("the root step", func(a *App) bool { return a.rootBusy() })
	h.press("q")
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "quit-question", s)
}

func TestFinishedScreen(t *testing.T) {
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		for _, s := range setupSteps {
			e := ev(step.StepDone, s.ID)
			e.Skipped = s.Phase == step.PhasePlatform
			emit(ctx, opts, ev(step.StepStarted, s.ID), e)
		}
		return nil
	})))
	h.until("the end", func(a *App) bool { return a.Finished() })
	s := snapshot(h)
	checkSize(t, s)
	golden(t, "finished", s)
}

func TestFixHints(t *testing.T) {
	for _, err := range []error{
		system.ErrLoginExpired, system.ErrNoPrivilegeTool, step.ErrDeclined, step.ErrStillFailing,
		errors.New("something else"),
	} {
		if Fix(fmt.Errorf("x: %w", err)) == "" {
			t.Errorf("no fix for %v", err)
		}
	}
	if got := Fix(hinted{}); got != "do the thing" {
		t.Errorf("an error's own fix: %q", got)
	}
}

type hinted struct{}

func (hinted) Error() string { return "hinted" }
func (hinted) Fix() string   { return "do the thing" }

func TestShellQuote(t *testing.T) {
	got := commandLine(system.Cmd{Name: "sh", Args: []string{"-c", "echo 'hi there'", "", "a$b"}})
	if got != `sh -c 'echo '\''hi there'\''' '' 'a$b'` {
		t.Errorf("commandLine = %s", got)
	}
}
