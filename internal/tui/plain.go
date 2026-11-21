package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	charmlog "charm.land/log/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// ErrNeedsYes reports a plan that needed approval when nobody could give
// it: plain mode without --yes and without a terminal to ask.
var ErrNeedsYes = errors.New("a step needs approval: run in a terminal, or pass --yes to approve every plan")

// PlainConfig sets up a plain run: lines instead of screens, for --plain,
// --yes, or when no terminal exists (spec §2).
type PlainConfig struct {
	Out io.Writer
	// In answers "Run it? [y/N]"; nil means nobody can answer, so any plan
	// that needs approval stops the run with ErrNeedsYes.
	In io.Reader
	// AssumeYes approves every plan (--yes).
	AssumeYes bool
	// Elevate runs the privilege login; nil leaves it unset.
	Elevate step.ElevateFunc
	Steps   []StepInfo
	Run     RunFunc
}

type plainConfirm struct {
	id, title string
	plan      step.Plan
	reply     chan bool
}

type plainDone struct{ err error }

// RunPlain runs the engine and prints a line per event. Events and approval
// questions all pass through this goroutine, so lines come out in the
// engine's order and never interleave.
func RunPlain(ctx context.Context, cfg PlainConfig) error {
	out := &colorprofile.Writer{Forward: cfg.Out, Profile: colorprofile.Detect(cfg.Out, os.Environ())}
	log := charmlog.NewWithOptions(out, charmlog.Options{ReportTimestamp: false})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan step.Event)
	asks := make(chan plainConfirm)
	done := make(chan plainDone, 1)
	opts := step.Options{Events: events, AssumeYes: cfg.AssumeYes, Elevate: cfg.Elevate}
	opts.Confirm = func(ctx context.Context, s step.Step, p step.Plan) (bool, error) {
		if cfg.In == nil {
			return false, ErrNeedsYes
		}
		req := plainConfirm{id: s.ID(), title: s.Title(), plan: p, reply: make(chan bool, 1)}
		select {
		case asks <- req:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		select {
		case ok := <-req.reply:
			return ok, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	go func() { done <- plainDone{cfg.Run(ctx, opts)} }()

	var in *bufio.Reader
	if cfg.In != nil {
		in = bufio.NewReader(cfg.In)
	}
	titles := map[string]string{}
	for _, s := range cfg.Steps {
		titles[s.ID] = s.Title
	}
	lastTenth := map[string]int{}
	for {
		select {
		case ev := <-events:
			title := titles[ev.StepID]
			if title == "" {
				title = ev.Title
			}
			printEvent(log, out, ev, title, lastTenth)
		case req := <-asks:
			fmt.Fprintf(out, "Run %s? [y/N] ", req.title)
			req.reply <- readYes(in)
		case d := <-done:
			// The engine's StepFailed event has already printed the cause,
			// ErrNeedsYes included.
			return d.err
		}
	}
}

func printEvent(log *charmlog.Logger, out io.Writer, ev step.Event, title string, lastTenth map[string]int) {
	prefix := ev.StepID + ": " + title
	switch ev.Kind {
	case step.StepDone:
		if ev.Skipped {
			log.Info(prefix+" already done", "detail", sanitise(ev.Status.Detail))
		} else {
			log.Info(prefix + " done")
		}
	case step.StepPlanned:
		if ev.Plan == nil {
			return
		}
		what := " planned:"
		if ev.Plan.NeedsRoot {
			what = " planned (needs root):"
		}
		log.Info(prefix + what)
		for _, l := range planLines(*ev.Plan) {
			fmt.Fprintln(out, l)
		}
	case step.NeedsSudo:
		log.Info(prefix + ": asking for the privilege login")
	case step.Progress:
		tenth := int(ev.Fraction * 10)
		if last, seen := lastTenth[ev.StepID]; seen && tenth <= last {
			return
		}
		lastTenth[ev.StepID] = tenth
		log.Info(fmt.Sprintf("%s: %s %d%%", ev.StepID, sanitise(ev.Message), int(ev.Fraction*100+0.5)))
	case step.StepFailed:
		msg := "unknown error"
		if ev.Err != nil {
			msg = sanitise(ev.Err.Error())
		}
		log.Error(prefix+" failed", "err", msg)
	}
}

// planLines lists a plan for plain output: # for root commands, $ for the
// user's own, as a shell prompt would show them.
func planLines(p step.Plan) []string {
	var lines []string
	for _, c := range p.Cmds {
		sigil := "$"
		if c.Root {
			sigil = "#"
		}
		lines = append(lines, "  "+sigil+" "+commandLine(c))
	}
	for _, f := range p.Files {
		lines = append(lines, fmt.Sprintf("  write %s (%04o, %d bytes)", visible(f.Path), f.Mode.Perm(), len(f.Content)))
	}
	return lines
}

// readYes reads one answer; only y or yes counts, and end of input means no.
func readYes(in *bufio.Reader) bool {
	if in == nil {
		return false
	}
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
