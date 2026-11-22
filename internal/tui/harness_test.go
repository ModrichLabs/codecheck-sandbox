package tui

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// harness runs a model the way a Program would, minus the terminal: each
// Cmd runs on its own goroutine and its message comes back through Update,
// so a test can look at the model between messages.
type harness struct {
	t    *testing.T
	m    tea.Model
	msgs chan tea.Msg
	quit bool
}

func start(t *testing.T, m tea.Model) *harness {
	t.Helper()
	h := &harness{t: t, m: m, msgs: make(chan tea.Msg, 256)}
	if s, ok := m.(interface{ Stop() }); ok {
		t.Cleanup(s.Stop)
	}
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.run(m.Init())
	return h
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			h.msgs <- msg
		}
	}()
}

func (h *harness) update(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
		return
	case tea.QuitMsg:
		h.quit = true
		return
	}
	m, cmd := h.m.Update(msg)
	h.m = m
	h.run(cmd)
}

func (h *harness) app() *App { return h.m.(*App) }

// press sends one key, named as tea.KeyPressMsg.String() names it.
func (h *harness) press(key string) {
	h.t.Helper()
	h.update(keyMsg(key))
}

func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(key)[0]
	return tea.KeyPressMsg{Code: r, Text: key}
}

// until feeds messages through Update until cond holds.
func (h *harness) until(what string, cond func(*App) bool) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond(h.app()) {
		select {
		case msg := <-h.msgs:
			h.update(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; screen:\n%s", what, h.screen())
		}
	}
}

// untilQuit feeds messages until the model asks to quit.
func (h *harness) untilQuit() {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for !h.quit {
		select {
		case msg := <-h.msgs:
			h.update(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting to quit; screen:\n%s", h.screen())
		}
	}
}

// screen returns the view as plain text.
func (h *harness) screen() string { return ansi.Strip(h.m.View().Content) }

// golden compares the screen with testdata/<name>.golden; -update rewrites it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file (go test -update rewrites it)\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// checkSize fails when any line of the screen runs past 100 columns or the
// screen past 30 lines.
func checkSize(t *testing.T, screen string) {
	t.Helper()
	lines := strings.Split(screen, "\n")
	if len(lines) > 30 {
		t.Errorf("screen holds %d lines, more than 30", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("line %d spans %d columns: %q", i+1, w, l)
		}
	}
}

// fakeStep satisfies step.Step for the interface's tests; the engine never
// runs it, since tests drive events by hand.
type fakeStep struct {
	id, title string
	phase     step.Phase
}

func (s fakeStep) ID() string                                 { return s.id }
func (s fakeStep) Title() string                              { return s.title }
func (s fakeStep) Phase() step.Phase                          { return s.phase }
func (s fakeStep) Check(context.Context) (step.Status, error) { return step.Status{}, nil }
func (s fakeStep) Plan(context.Context) (step.Plan, error)    { return step.Plan{}, nil }
func (s fakeStep) Apply(context.Context) error                { return nil }
func (s fakeStep) Undo(context.Context, []step.Item) error    { return nil }

// setupSteps mirrors a slice of the real setup.
var setupSteps = []StepInfo{
	{ID: "platform-kvm", Title: "KVM access", Phase: step.PhasePlatform},
	{ID: "components", Title: "Install packages", Phase: step.PhaseComponents},
	{ID: "host-user", Title: "fcjail user", Phase: step.PhaseHost},
	{ID: "host-tap", Title: "Review tap cc-tap0", Phase: step.PhaseHost},
	{ID: "host-rules", Title: "Firewall rules (nftables)", Phase: step.PhaseHost},
	{ID: "host-proxy", Title: "Egress proxy service", Phase: step.PhaseHost},
	{ID: "guest-image", Title: "Guest image", Phase: step.PhaseGuest},
	{ID: "verify-selftest", Title: "Selftest", Phase: step.PhaseVerify},
}

var setupSidebar = []SidebarEntry{
	{Title: "Welcome"},
	{Title: "Platform", Phase: step.PhasePlatform},
	{Title: "Components", Phase: step.PhaseComponents},
	{Title: "Your details"},
	{Title: "Host network", Phase: step.PhaseHost},
	{Title: "Guest build", Phase: step.PhaseGuest},
	{Title: "Verify", Phase: step.PhaseVerify},
	{Title: "Summary"},
}

func testConfig(run RunFunc) Config {
	return Config{
		Title:   "CodeCheck Sandbox setup",
		Theme:   theme.New(true),
		Sidebar: setupSidebar,
		Steps:   setupSteps,
		Run:     run,
	}
}

// emit sends events the way the engine does: blocking, until ctx ends.
func emit(ctx context.Context, opts step.Options, evs ...step.Event) {
	for _, ev := range evs {
		select {
		case opts.Events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

func ev(kind step.EventKind, id string) step.Event {
	e := step.Event{Kind: kind, StepID: id}
	for _, s := range setupSteps {
		if s.ID == id {
			e.Title = s.Title
		}
	}
	return e
}
