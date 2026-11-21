package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// confirmReq carries one engine request to approve a plan.
type confirmReq struct {
	id, title string
	plan      step.Plan
	reply     chan bool
}

// confirmState holds the confirm panel while it waits for an answer.
type confirmState struct {
	req   confirmReq
	files bool // the d view of every command and file in full
	vp    viewport.Model
}

// confirm implements step.ConfirmFunc: it hands the plan to the interface
// and waits for Enter or s.
func (a *App) confirmPlan(ctx context.Context, s step.Step, p step.Plan) (bool, error) {
	req := confirmReq{id: s.ID(), title: s.Title(), plan: p, reply: make(chan bool, 1)}
	select {
	case a.requests <- req:
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

func (a *App) openConfirm(req confirmReq) {
	a.current, a.root = req.id, req.plan.NeedsRoot
	a.states[req.id] = running
	a.confirm = &confirmState{req: req}
}

func (a *App) confirmKey(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := a.confirm
	if c.files {
		switch key {
		case "d", "esc":
			c.files = false
			return a, nil
		case "q", "ctrl+c":
			return a.quit()
		}
		var cmd tea.Cmd
		c.vp, cmd = c.vp.Update(msg)
		return a, cmd
	}
	switch key {
	case "enter":
		c.req.reply <- true
		a.confirm = nil
	case "s":
		c.req.reply <- false
		a.confirm = nil
	case "d":
		c.files = true
		c.vp = viewport.New(viewport.WithWidth(a.mainWidth()), viewport.WithHeight(max(a.bodyRows(), 3)))
		c.vp.SetContent(planDetails(c.req.plan))
	case "q", "ctrl+c":
		return a.quit()
	}
	return a, nil
}

// planTitle heads the confirm panel.
func planTitle(p step.Plan) string {
	if p.NeedsRoot {
		return "Needs root: review before running"
	}
	return "Review before running"
}

// planSummary lists one line per command and file, cut to rows lines.
func planSummary(p step.Plan, rows int) []string {
	var lines []string
	for _, c := range p.Cmds {
		lines = append(lines, commandLine(c))
	}
	for _, f := range p.Files {
		lines = append(lines, fmt.Sprintf("write %s (%04o, %d bytes)", f.Path, f.Mode.Perm(), len(f.Content)))
	}
	if len(lines) == 0 {
		lines = []string{"Nothing to run or write."}
	}
	if rows > 0 && len(lines) > rows {
		more := len(lines) - rows + 1
		lines = append(lines[:rows-1], fmt.Sprintf("… and %d more; [d] shows everything", more))
	}
	return lines
}

// planDetails shows every command and every file's exact bytes, for the
// d view.
func planDetails(p step.Plan) string {
	var b strings.Builder
	if len(p.Cmds) > 0 {
		b.WriteString("Commands\n\n")
		for _, c := range p.Cmds {
			b.WriteString("  " + commandLine(c) + "\n")
			if len(c.Stdin) > 0 {
				fmt.Fprintf(&b, "    (with %d bytes on stdin)\n", len(c.Stdin))
			}
		}
	}
	for _, f := range p.Files {
		fmt.Fprintf(&b, "\n── %s (%04o) ──\n", f.Path, f.Mode.Perm())
		b.WriteString(strings.TrimRight(string(f.Content), "\n") + "\n")
	}
	return strings.TrimLeft(b.String(), "\n")
}

// commandLine renders a command as a shell would read it.
func commandLine(c system.Cmd) string {
	parts := []string{shellQuote(c.Name)}
	for _, a := range c.Args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
