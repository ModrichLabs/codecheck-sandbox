package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// Uninstall lists what uninstall removes and what it keeps, and asks once.
type Uninstall struct {
	th              theme.Theme
	items           []step.Item
	reports         string
	purge           bool
	done, confirmed bool
	width, height   int
}

// NewUninstall builds the list from the manifest's items; reports names the
// reports folder, which only purge removes.
func NewUninstall(th theme.Theme, items []step.Item, reports string, purge bool) *Uninstall {
	return &Uninstall{th: th, items: items, reports: reports, purge: purge, width: 100, height: 30}
}

// Done reports an answered screen; Confirmed, a yes.
func (u *Uninstall) Done() bool      { return u.done }
func (u *Uninstall) Confirmed() bool { return u.confirmed }

// Init implements tea.Model.
func (u *Uninstall) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (u *Uninstall) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			u.done, u.confirmed = true, true
			return u, tea.Quit
		case "q", "ctrl+c", "esc", "n":
			u.done = true
			return u, tea.Quit
		}
	}
	return u, nil
}

func itemLine(it step.Item) string {
	what := it.Path
	if what == "" {
		what = it.Name
	}
	return "  " + string(it.Kind) + " " + sanitise(what)
}

// View implements tea.Model.
func (u *Uninstall) View() tea.View {
	lines := []string{u.th.Heading.Render("These go:")}
	var keep []string
	for _, it := range u.items {
		if it.State == step.Created {
			lines = append(lines, itemLine(it))
		} else {
			keep = append(keep, itemLine(it)+u.th.Faint.Render("  (here before setup)"))
		}
	}
	if u.purge {
		lines = append(lines, "  reports "+u.reports)
	} else {
		keep = append(keep, "  "+u.reports+u.th.Faint.Render("  (your reports; --purge removes them)"))
	}
	lines = append(lines, "", u.th.Heading.Render("These stay:"))
	lines = append(lines, keep...)
	rows := max(u.height-3, 1)
	if len(lines) > rows {
		lines = append(lines[:rows-1], u.th.Faint.Render("  …"))
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	lines = append(lines, u.th.KeyHelp(theme.Key{Name: "Enter", Does: "uninstall"}, theme.Key{Name: "q", Does: "cancel"}))
	v := tea.NewView(u.th.Panel("CodeCheck Sandbox uninstall", strings.Join(lines, "\n"), u.width))
	v.AltScreen = true
	return v
}
