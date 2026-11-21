// Package tui holds the Bubble Tea interface: one root model that runs the
// step engine on its own goroutine and turns its events, confirm requests and
// privilege handovers into screens.
package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// ErrQuit reports a run the user stopped from the interface.
var ErrQuit = errors.New("quit from the interface")

// RunFunc runs the engine with the options the interface fills in: Events,
// Confirm and Elevate. Production passes a closure over step.Run.
type RunFunc func(ctx context.Context, opts step.Options) error

// StepInfo names one step the run may take, in order.
type StepInfo struct {
	ID, Title string
	Phase     step.Phase
}

// SidebarEntry names one sidebar line. An entry without a Phase stands for
// a part of the flow outside the engine, such as the welcome screen.
type SidebarEntry struct {
	Title string
	Phase step.Phase
}

// Config sets up an App.
type Config struct {
	Title   string
	Theme   theme.Theme
	Sidebar []SidebarEntry
	Steps   []StepInfo
	Run     RunFunc
}

// App drives one engine run.
type App struct {
	cfg    Config
	th     theme.Theme
	width  int
	height int

	ctx    context.Context
	cancel context.CancelFunc
	events chan step.Event
	done   chan error

	states  map[string]theme.State
	details map[string]string
	current string // the step the engine works on
	root    bool   // the current step's plan needs root

	finished       bool
	err            error
	confirmingQuit bool
}

// New builds the interface for one run.
func New(cfg Config) *App {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{
		cfg: cfg, th: cfg.Theme, width: 100, height: 30,
		ctx: ctx, cancel: cancel,
		events: make(chan step.Event), done: make(chan error, 1),
		states: map[string]theme.State{}, details: map[string]string{},
	}
	return a
}

// Init starts the engine.
func (a *App) Init() tea.Cmd {
	a.startRun()
	return a.listen()
}

// Stop ends the engine run, if any; the interface stays as it was.
func (a *App) Stop() { a.cancel() }

// Err returns how the run ended: nil, the engine's error or ErrQuit.
func (a *App) Err() error { return a.err }

// Finished reports a run that has ended.
func (a *App) Finished() bool { return a.finished }

// StepState says how far a step has got.
func (a *App) StepState(id string) theme.State { return a.states[id] }

func (a *App) startRun() {
	opts := step.Options{Events: a.events}
	ctx, run := a.ctx, a.cfg.Run
	go func() { a.done <- run(ctx, opts) }()
}

type runDoneMsg struct{ err error }

// listen waits for the engine's next event, or the end of the run. One
// listener runs at a time, and the engine blocks on each event until it gets
// read, so every event reaches Update before the run's end does.
func (a *App) listen() tea.Cmd {
	events, done, ctx := a.events, a.done, a.ctx
	return func() tea.Msg {
		select {
		case ev := <-events:
			return ev
		case err := <-done:
			return runDoneMsg{err}
		case <-ctx.Done():
			return nil
		}
	}
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case step.Event:
		a.apply(msg)
		return a, a.listen()
	case runDoneMsg:
		a.finished, a.err, a.root = true, msg.err, false
	case tea.KeyPressMsg:
		return a.key(msg)
	}
	return a, nil
}

func (a *App) apply(ev step.Event) {
	switch ev.Kind {
	case step.StepStarted:
		a.current, a.root = ev.StepID, false
		a.states[ev.StepID] = theme.Running
	case step.StepPlanned:
		if ev.Plan != nil {
			a.root = ev.Plan.NeedsRoot
		}
	case step.StepDone:
		a.states[ev.StepID] = theme.Done
		if ev.Skipped {
			a.states[ev.StepID] = theme.Skipped
		}
		a.details[ev.StepID] = ev.Status.Detail
		a.root = false
	case step.StepFailed:
		a.states[ev.StepID] = theme.Failed
		if ev.Err != nil {
			a.details[ev.StepID] = ev.Err.Error()
		}
		a.root = false
	}
}

// rootBusy reports a root step under way, which quitting would cut short.
func (a *App) rootBusy() bool {
	return !a.finished && a.root && a.states[a.current] == theme.Running
}

func (a *App) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if a.confirmingQuit {
		switch key {
		case "y", "Y":
			return a.quit()
		case "n", "N", "esc":
			a.confirmingQuit = false
		}
		return a, nil
	}
	switch key {
	case "q", "ctrl+c":
		if a.rootBusy() {
			a.confirmingQuit = true
			return a, nil
		}
		return a.quit()
	case "enter":
		if a.finished {
			return a, tea.Quit
		}
	}
	return a, nil
}

func (a *App) quit() (tea.Model, tea.Cmd) {
	if !a.finished {
		a.err = ErrQuit
	}
	a.cancel()
	return a, tea.Quit
}

// View implements tea.Model.
func (a *App) View() tea.View {
	var b strings.Builder
	b.WriteString(a.th.Title.Render(a.cfg.Title) + "\n\n")
	for _, s := range a.cfg.Steps {
		b.WriteString(a.th.Mark(a.states[s.ID]) + " " + s.Title + "\n")
	}
	if a.confirmingQuit {
		b.WriteString("\nA root step is running. Quit anyway? [y] [n]\n")
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}
