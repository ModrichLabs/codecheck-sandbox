package tui

import (
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// sidebarWidth holds the sidebar column's width, without its divider.
const sidebarWidth = 18

// phaseState sums up the steps of one phase; ok stays false for a phase
// with no steps in this run.
func (a *App) phaseState(p step.Phase) (s theme.State, ok bool) {
	all := true
	for _, st := range a.cfg.Steps {
		if st.Phase != p {
			continue
		}
		ok = true
		switch a.states[st.ID] {
		case theme.Failed:
			return theme.Failed, true
		case theme.Done, theme.Skipped:
		default:
			all = false
		}
	}
	switch {
	case !ok:
		return theme.Pending, false
	case a.currentPhase() == p && !(a.finished && a.err == nil):
		return theme.Current, true
	case all:
		return theme.Done, true
	}
	return theme.Pending, true
}

// entryState works out a sidebar entry's state. Entries outside the engine
// (welcome, your details, summary) count as done once a later entry has
// started.
func (a *App) entryState(i int) theme.State {
	if s, ok := a.phaseState(a.cfg.Sidebar[i].Phase); ok && a.cfg.Sidebar[i].Phase != "" {
		return s
	}
	if a.finished && a.err == nil {
		return theme.Done
	}
	for j := i + 1; j < len(a.cfg.Sidebar); j++ {
		p := a.cfg.Sidebar[j].Phase
		if p == "" {
			continue
		}
		if s, ok := a.phaseState(p); ok && s != theme.Pending {
			return theme.Done
		}
	}
	return theme.Pending
}

// currentPhase names the phase of the step under way, or the first phase
// before any step starts.
func (a *App) currentPhase() step.Phase {
	for _, s := range a.cfg.Steps {
		if s.ID == a.current {
			return s.Phase
		}
	}
	for _, e := range a.cfg.Sidebar {
		if e.Phase != "" {
			return e.Phase
		}
	}
	return ""
}

func (a *App) sidebarLines() []string {
	lines := make([]string, len(a.cfg.Sidebar))
	for i, e := range a.cfg.Sidebar {
		st := a.entryState(i)
		title := a.th.Body.Render(e.Title)
		switch st {
		case theme.Current:
			title = a.th.Selected.Render(e.Title)
		case theme.Pending:
			title = a.th.Faint.Render(e.Title)
		}
		lines[i] = a.th.Mark(st) + " " + title
	}
	return lines
}

// stepLines lists the current phase's steps; the running one spins.
func (a *App) stepLines() []string {
	p := a.currentPhase()
	var lines []string
	for _, s := range a.cfg.Steps {
		if s.Phase != p {
			continue
		}
		st := a.states[s.ID]
		mark := a.th.Mark(st)
		if st == theme.Running && !a.finished {
			mark = a.spin.View()
		}
		title := a.th.Body.Render(s.Title)
		if st == theme.Pending {
			title = a.th.Faint.Render(s.Title)
		}
		lines = append(lines, mark+" "+title)
	}
	return lines
}

// phaseTitle names the current phase as the sidebar does.
func (a *App) phaseTitle() string {
	p := a.currentPhase()
	for _, e := range a.cfg.Sidebar {
		if e.Phase == p {
			return e.Title
		}
	}
	return string(p)
}
