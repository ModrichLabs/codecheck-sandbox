// Package tui holds the Bubble Tea interface: one root model that runs the
// step engine on its own goroutine and turns its events, confirm requests and
// privilege handovers into screens.
package tui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	// Handover runs the privilege login before root steps; nil leaves the
	// engine's Elevate unset (dry runs, and tests).
	Handover *Handover
}

const (
	running = theme.Running
	failed  = theme.Failed
)

// App drives one engine run.
type App struct {
	cfg    Config
	th     theme.Theme
	width  int
	height int

	ctx      context.Context
	cancel   context.CancelFunc
	events   chan step.Event
	requests chan any // confirmReq, elevateReq
	done     chan error

	states   map[string]theme.State
	current  string // the step the engine works on
	root     bool   // the current step's plan needs root
	spin     spinner.Model
	progress progressState
	confirm  *confirmState

	finished       bool
	err            error
	confirmingQuit bool
	elevating      bool // the privilege tool has the terminal
	stopKeepAlive  context.CancelFunc
}

// New builds the interface for one run.
func New(cfg Config) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		cfg: cfg, th: cfg.Theme, width: 100, height: 30,
		ctx: ctx, cancel: cancel,
		events: make(chan step.Event), requests: make(chan any), done: make(chan error, 1),
		states: map[string]theme.State{},
		spin:   newSpinner(cfg.Theme),
	}
}

// Init starts the engine.
func (a *App) Init() tea.Cmd {
	a.startRun()
	return tea.Batch(a.listen(), a.spin.Tick)
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
	opts := step.Options{Events: a.events, Confirm: a.confirmPlan}
	if a.cfg.Handover != nil {
		opts.Elevate = a.elevate
	}
	ctx, run := a.ctx, a.cfg.Run
	go func() { a.done <- run(ctx, opts) }()
}

type runDoneMsg struct{ err error }

// listen waits for the engine's next event or request, or the end of the
// run. One listener runs at a time, and the engine blocks on each event and
// request until it gets read, so everything reaches Update in the order the
// engine sent it, and before the run's end.
func (a *App) listen() tea.Cmd {
	events, requests, done, ctx := a.events, a.requests, a.done, a.ctx
	return func() tea.Msg {
		select {
		case ev := <-events:
			return ev
		case req := <-requests:
			return req
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
	case spinner.TickMsg:
		if a.finished {
			return a, nil
		}
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		return a, cmd
	case step.Event:
		a.apply(msg)
		return a, a.listen()
	case confirmReq:
		a.openConfirm(msg)
		return a, a.listen()
	case elevateReq:
		return a, tea.Batch(a.listen(), a.startLogin(msg))
	case loginDoneMsg:
		a.loginDone(msg)
	case runDoneMsg:
		a.finished, a.err, a.root, a.confirm = true, msg.err, false, nil
	case tea.KeyPressMsg:
		return a.key(msg)
	}
	return a, nil
}

func (a *App) apply(ev step.Event) {
	switch ev.Kind {
	case step.StepStarted:
		a.current, a.root, a.progress = ev.StepID, false, progressState{}
		a.states[ev.StepID] = running
	case step.StepPlanned:
		if ev.Plan != nil {
			a.root = ev.Plan.NeedsRoot
		}
	case step.Progress:
		a.progress = progressState{msg: ev.Message, frac: ev.Fraction}
	case step.StepDone:
		a.states[ev.StepID] = theme.Done
		if ev.Skipped {
			a.states[ev.StepID] = theme.Skipped
		}
		a.root, a.progress = false, progressState{}
	case step.StepFailed:
		a.states[ev.StepID] = failed
		a.root, a.progress = false, progressState{}
	}
}

// rootBusy reports a root step under way, which quitting would cut short.
// A plan still waiting for approval has run nothing yet.
func (a *App) rootBusy() bool {
	return !a.finished && a.confirm == nil && a.root && a.states[a.current] == running
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
	if a.confirm != nil {
		return a.confirmKey(key, k)
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
	case "r":
		if a.finished && a.err != nil && !errors.Is(a.err, ErrQuit) {
			return a, a.retry()
		}
	}
	return a, nil
}

// retry runs the engine again. Steps that finished pass their checks and
// get skipped, so the run picks up at the failed step.
func (a *App) retry() tea.Cmd {
	for id, s := range a.states {
		if s == failed || s == running {
			a.states[id] = theme.Pending
		}
	}
	a.finished, a.err = false, nil
	a.startRun()
	return tea.Batch(a.listen(), a.spin.Tick)
}

func (a *App) quit() (tea.Model, tea.Cmd) {
	if !a.finished {
		a.err = ErrQuit
	}
	a.confirm = nil
	a.cancel()
	return a, tea.Quit
}

// --- Layout ---

// innerWidth spans the frame's inside; mainWidth the pane right of the
// sidebar and its " │ " divider.
func (a *App) innerWidth() int { return max(a.width-4, 20) }
func (a *App) mainWidth() int  { return max(a.innerWidth()-sidebarWidth-3, 10) }
func (a *App) innerRows() int  { return max(a.height-2, 5) }

// bodyRows leaves room in the main pane for the heading, its gap, the step
// list, a gap and the help line.
func (a *App) bodyRows() int {
	return a.innerRows() - 2 - len(a.stepLines()) - 1 - 2
}

// View implements tea.Model.
func (a *App) View() tea.View {
	side := a.sidebarLines()
	main := a.mainLines()
	rows := a.innerRows()
	divider := a.th.Faint.Render("│")
	var b strings.Builder
	for i := range rows {
		var l, r string
		if i < len(side) {
			l = side[i]
		}
		if i < len(main) {
			r = main[i]
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(pad(l, sidebarWidth) + " " + divider + " " + ansi.Truncate(r, a.mainWidth(), "…"))
	}
	v := tea.NewView(a.th.Panel(a.cfg.Title, b.String(), a.width))
	v.AltScreen = true
	return v
}

// mainLines fills the main pane: heading, steps, the screen's own body, and
// the help line pinned to the bottom.
func (a *App) mainLines() []string {
	lines := []string{a.th.Heading.Render(a.phaseTitle()), ""}
	lines = append(lines, a.stepLines()...)
	lines = append(lines, "")
	rows := a.bodyRows()
	w := a.mainWidth()
	var body []string
	switch {
	case a.confirm != nil && a.confirm.files:
		body = strings.Split(a.confirm.vp.View(), "\n")
	case a.confirm != nil:
		p := a.confirm.req.plan
		summary := strings.Join(planSummary(p, max(rows-2, 1)), "\n")
		panel := a.th.Panel
		if p.NeedsRoot {
			panel = a.th.AlertPanel
		}
		body = strings.Split(panel(planTitle(p), summary, w), "\n")
	case a.finished && a.err != nil && !errors.Is(a.err, ErrQuit):
		body = a.failureLines(w)
	case a.finished && a.err == nil:
		body = []string{a.th.Mark(theme.Done) + " " + a.th.Heading.Render("All steps done.")}
	case a.elevating:
		body = []string{a.th.Warning.Render("Waiting for your password in the terminal…"),
			a.th.Faint.Render("Root commands after this one never prompt; the login stays fresh until setup ends.")}
	default:
		body = a.progressLines()
	}
	if len(body) > rows {
		body = body[:rows]
	}
	lines = append(lines, body...)
	for len(lines) < a.innerRows()-1 {
		lines = append(lines, "")
	}
	return append(lines, a.helpLine())
}

func (a *App) helpLine() string {
	k := func(name, does string) theme.Key { return theme.Key{Name: name, Does: does} }
	switch {
	case a.confirmingQuit:
		return a.th.Warning.Render("Quitting now could leave a root step half done. Quit anyway?") + "  " +
			a.th.KeyHelp(k("y", "quit"), k("n", "keep going"))
	case a.confirm != nil && a.confirm.files:
		return a.th.KeyHelp(k("↑/↓", "scroll"), k("d", "back"), k("q", "quit"))
	case a.confirm != nil:
		return a.th.KeyHelp(k("Enter", "run"), k("d", "show files"), k("s", "skip"), k("q", "quit"))
	case a.finished && a.err == nil:
		return a.th.KeyHelp(k("Enter", "finish"))
	case a.finished && !errors.Is(a.err, ErrQuit):
		return a.th.KeyHelp(k("r", "retry"), k("q", "quit"))
	}
	return a.th.KeyHelp(k("q", "quit"))
}

// pad fills s with spaces to width columns.
func pad(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(width-ansi.StringWidth(s), 0))
}
