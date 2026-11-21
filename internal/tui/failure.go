package tui

import (
	"errors"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Fixer lets an error say how to fix it; the failure screen shows the fix.
type Fixer interface {
	error
	Fix() string
}

// Fix suggests what to do about err.
func Fix(err error) string {
	if f, ok := errors.AsType[Fixer](err); ok {
		return f.Fix()
	}
	switch {
	case errors.Is(err, ErrLoginFailed):
		return "The password didn't get accepted, or the prompt got cancelled. Press r to try again."
	case errors.Is(err, system.ErrLoginExpired):
		return "The privilege login ran out. Press r to retry; you'll get asked for your password again."
	case errors.Is(err, system.ErrNoPrivilegeTool):
		return "Install sudo (or sudo-rs, doas or run0), then press r to retry."
	case errors.Is(err, step.ErrDeclined):
		return "You skipped this step, and later steps need it. Press r to see its plan again."
	case errors.Is(err, step.ErrStillFailing):
		return "The step ran, but its check still fails. Run codecheck-sandbox doctor for details."
	}
	return "Read the log in ~/.local/state/codecheck-sandbox/log/, or run again with --debug. Press r to retry."
}

// failureLines explains a failed run: which step, why, and what to do.
func (a *App) failureLines(width int) []string {
	title := "Setup stopped"
	for _, s := range a.cfg.Steps {
		if a.states[s.ID] == failed {
			title = s.Title + " failed"
		}
	}
	wrap := lipgloss.NewStyle().Width(width - 4)
	body := a.th.Error.Render("Cause:") + " " + a.err.Error() + "\n\n" +
		a.th.Heading.Render("Fix:") + " " + Fix(a.err)
	return strings.Split(a.th.AlertPanel(a.th.Mark(failed)+" "+title, wrap.Render(body), width), "\n")
}
