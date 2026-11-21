package tui

import (
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// progressState holds the latest Progress event of the running step.
type progressState struct {
	msg  string
	frac float64
}

func newSpinner(th theme.Theme) spinner.Model {
	return spinner.New(
		spinner.WithSpinner(spinner.Spinner{
			Frames: []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"},
			FPS:    time.Second / 10,
		}),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(th.Accent)),
	)
}

func (a *App) progressLines() []string {
	if a.progress.msg == "" {
		return nil
	}
	bar := progress.New(progress.WithWidth(min(a.mainWidth(), 60)), progress.WithColors(a.th.Accent))
	return []string{a.th.Body.Render(a.progress.msg), bar.ViewAs(a.progress.frac)}
}
