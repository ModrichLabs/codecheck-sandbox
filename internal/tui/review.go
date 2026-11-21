package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// ReviewFunc runs one review (review.Pipeline.Review), reporting each state.
type ReviewFunc func(ctx context.Context, status func(review.Status)) (review.RunResult, error)

// maxReport caps how much of report.md the screen reads.
const maxReport = 4 << 20

// reviewStates lists the live states in order, for the status list.
var reviewStates = []review.State{review.StatePreparing, review.StateBooting, review.StateRunning,
	review.StateFinished, review.StateCollecting}

// Review shows a review's live status, then its report (spec §2, live
// status). Cost and sub-agent counts only exist once the VM has finished,
// so they appear with the report.
type Review struct {
	th     theme.Theme
	target string
	run    ReviewFunc
	now    func() time.Time

	ctx      context.Context
	cancel   context.CancelFunc
	statuses chan review.Status
	finish   chan reviewDoneMsg

	state   review.State
	started time.Time
	ended   time.Time // when the run returned; the clock stops there
	spin    spinner.Model

	done           bool
	stopping       bool
	confirmingQuit bool
	result         review.RunResult
	err            error
	vp             viewport.Model
	width, height  int
}

type reviewDoneMsg struct {
	result review.RunResult
	err    error
}

type clockMsg struct{}

// NewReview builds the screen for one run; target names what gets reviewed.
func NewReview(th theme.Theme, target string, run ReviewFunc) *Review {
	ctx, cancel := context.WithCancel(context.Background())
	return &Review{th: th, target: target, run: run, now: time.Now, ctx: ctx, cancel: cancel,
		statuses: make(chan review.Status), finish: make(chan reviewDoneMsg, 1),
		spin: newSpinner(th), width: 100, height: 30}
}

// Err returns how the run ended.
func (r *Review) Err() error { return r.err }

// Result returns the finished run's result.
func (r *Review) Result() review.RunResult { return r.result }

// Stop cancels the run; the pipeline still cleans up before it returns.
func (r *Review) Stop() { r.cancel() }

// Init implements tea.Model.
func (r *Review) Init() tea.Cmd {
	ctx, statuses := r.ctx, r.statuses
	report := func(s review.Status) {
		select {
		case statuses <- s:
		case <-ctx.Done():
		}
	}
	go func() {
		res, err := r.run(ctx, report)
		r.finish <- reviewDoneMsg{res, err}
	}()
	return tea.Batch(r.listen(), r.spin.Tick, r.clock())
}

// listen reads the next state or the end of the run; the pipeline blocks
// on each state until read, so the end always comes last.
func (r *Review) listen() tea.Cmd {
	statuses, finish := r.statuses, r.finish
	return func() tea.Msg {
		select {
		case s := <-statuses:
			return s
		case d := <-finish:
			return d
		}
	}
}

func (r *Review) clock() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return clockMsg{} })
}

// Update implements tea.Model.
func (r *Review) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.width, r.height = msg.Width, msg.Height
		if r.done {
			r.showReport()
		}
	case review.Status:
		r.state = msg.State
		if r.started.IsZero() {
			r.started = msg.Started
		}
		return r, r.listen()
	case reviewDoneMsg:
		r.done, r.result, r.err, r.ended = true, msg.result, msg.err, r.now()
		r.showReport()
		if r.stopping {
			return r, tea.Quit
		}
	case clockMsg:
		if !r.done {
			return r, r.clock()
		}
	case spinner.TickMsg:
		if r.done {
			return r, nil
		}
		var cmd tea.Cmd
		r.spin, cmd = r.spin.Update(msg)
		return r, cmd
	case tea.KeyPressMsg:
		return r.key(msg)
	}
	return r, nil
}

func (r *Review) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	switch {
	case r.done && (key == "q" || key == "ctrl+c" || key == "esc"):
		return r, tea.Quit
	case r.done:
		var cmd tea.Cmd
		r.vp, cmd = r.vp.Update(k)
		return r, cmd
	case r.confirmingQuit && (key == "y" || key == "Y"):
		// Cancel, then wait for the pipeline: it shuts the VM down and
		// runs cc-launch clean before it returns.
		r.confirmingQuit, r.stopping = false, true
		r.cancel()
	case r.confirmingQuit && (key == "n" || key == "N" || key == "esc"):
		r.confirmingQuit = false
	case !r.stopping && (key == "q" || key == "ctrl+c"):
		r.confirmingQuit = true
	}
	return r, nil
}

// showReport renders report.md into the viewport.
func (r *Review) showReport() {
	rows := max(r.height-2-len(r.summaryLines())-2, 3)
	r.vp = viewport.New(viewport.WithWidth(r.width-4), viewport.WithHeight(rows))
	if r.err != nil || r.result.ReportDir == "" {
		return
	}
	text, err := readCapped(filepath.Join(r.result.ReportDir, "report.md"), maxReport)
	if err != nil {
		r.vp.SetContent(r.th.Error.Render("Couldn't read report.md: " + err.Error()))
		return
	}
	style := "light"
	if r.th.IsDark {
		style = "dark"
	}
	out := sanitise(text)
	if g, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(r.width-8)); err == nil {
		if rendered, err := g.Render(out); err == nil {
			out = rendered
		}
	}
	r.vp.SetContent(strings.Trim(out, "\n"))
}

func readCapped(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit))
	return string(b), err
}

func (r *Review) summaryLines() []string {
	if r.err != nil {
		return []string{r.th.Error.Render("The review failed: ") + sanitise(r.err.Error())}
	}
	s := r.result.Summary
	head := r.th.Heading.Render("Outcome: "+string(s.Outcome)) + r.th.Faint.Render(fmt.Sprintf(
		"  ·  US$%.2f  ·  %d sub-agents  ·  %s", s.Result.CostUSD, s.SubAgents, r.elapsed()))
	lines := []string{head}
	for _, w := range s.Warnings {
		lines = append(lines, r.th.Warning.Render("! "+sanitise(w)))
	}
	if r.result.ReportDir != "" {
		lines = append(lines, r.th.Faint.Render("Saved in "+sanitise(r.result.ReportDir)))
	}
	return lines
}

// View implements tea.Model.
func (r *Review) View() tea.View {
	var body []string
	k := func(n, d string) theme.Key { return theme.Key{Name: n, Does: d} }
	var help string
	if r.done {
		body = append(r.summaryLines(), "")
		body = append(body, strings.Split(r.vp.View(), "\n")...)
		help = r.th.KeyHelp(k("↑/↓", "scroll"), k("q", "quit"))
	} else {
		body = r.statusLines()
		help = r.th.KeyHelp(k("q", "stop"))
		switch {
		case r.stopping:
			help = r.th.Warning.Render("Stopping: shutting the VM down and cleaning up…")
		case r.confirmingQuit:
			help = r.th.Warning.Render("Stop the review? The VM gets shut down.") + "  " +
				r.th.KeyHelp(k("y", "stop"), k("n", "keep going"))
		}
	}
	rows := max(r.height-3, 1)
	if len(body) > rows {
		body = body[:rows]
	}
	for len(body) < rows {
		body = append(body, "")
	}
	body = append(body, help)
	v := tea.NewView(r.th.Panel("CodeCheck review", strings.Join(body, "\n"), r.width))
	v.AltScreen = true
	return v
}

func (r *Review) statusLines() []string {
	lines := []string{
		r.th.Heading.Render(sanitise(r.target)),
		"",
		r.th.Body.Render(fmt.Sprintf("%s %s", r.state, r.elapsed())),
		"",
	}
	reached := true
	for _, s := range reviewStates {
		mark := r.th.Mark(theme.Pending)
		switch {
		case s == r.state:
			mark, reached = r.spin.View(), false
		case reached && r.state != "":
			mark = r.th.Mark(theme.Done)
		}
		lines = append(lines, mark+" "+string(s))
	}
	return append(lines, "", r.th.Faint.Render("Cost and sub-agent counts arrive once the VM finishes."))
}

// elapsed gives the run's time so far, frozen once it ends, and zero before
// the first status says when it started.
func (r *Review) elapsed() string {
	if r.started.IsZero() {
		return elapsed(0)
	}
	end := r.now()
	if !r.ended.IsZero() {
		end = r.ended
	}
	return elapsed(end.Sub(r.started))
}

// elapsed formats a duration as 7m05s or 1h02m.
func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// sanitise makes text from outside (the report, error messages) safe to
// draw: escape sequences go whole, then stray control characters, invalid
// bytes and the bidirectional overrides that can hide text.
func sanitise(s string) string {
	s = ansi.Strip(strings.ToValidUTF8(s, ""))
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		switch {
		case r == '\n' || r == '\t':
		case unicode.IsControl(r): // C0, DEL and C1
			continue
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
