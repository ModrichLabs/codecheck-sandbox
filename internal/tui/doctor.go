package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// DoctorRow holds one check's result.
type DoctorRow struct {
	Name   string
	OK     bool
	Detail string
}

// DoctorView renders doctor's results as a table with a tally. It prints
// once, so it suits plain output as well as a terminal.
func DoctorView(th theme.Theme, rows []DoctorRow, width int) string {
	passed := 0
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(th.BorderColor)).
		Headers("Check", "Detail").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return s.Bold(true).Foreground(th.Text)
			}
			return s
		})
	for _, r := range rows {
		mark := th.Mark(theme.Failed)
		if r.OK {
			mark = th.Mark(theme.Done)
			passed++
		}
		t.Row(mark+" "+r.Name, sanitise(r.Detail))
	}
	failed := len(rows) - passed
	tally := th.Body.Render(fmt.Sprintf("%d passed, %d failed", passed, failed))
	if failed > 0 {
		tally = th.Error.Render(fmt.Sprintf("%d passed, %d failed", passed, failed)) +
			th.Faint.Render(" · fix the failed checks, then run doctor again")
	}
	out := t.String()
	if lipgloss.Width(out) > width { // cap only; a narrow table stays narrow
		out = t.Width(width).String()
	}
	return out + "\n" + tally
}
