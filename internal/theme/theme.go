// Package theme holds the interface's colours, glyphs and shared styles. The
// palette adapts to light and dark terminals; on a terminal without colour
// the glyphs alone carry each state.
package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// State names how far a phase or step has got.
type State int

const (
	Pending State = iota
	Current
	Running
	Done
	Skipped
	Failed
)

// Glyphs for each state. Running shows a still spinner frame; live screens
// animate it with a bubbles spinner of the same family.
const (
	GlyphPending = "·"
	GlyphCurrent = "▸"
	GlyphRunning = "⣾"
	GlyphDone    = "✓"
	GlyphFailed  = "✗"
)

// Theme carries the palette and styles for one terminal background.
type Theme struct {
	IsDark bool

	Accent, Muted, Text, OK, Warn, Bad color.Color

	Title       lipgloss.Style // the app frame's title
	Heading     lipgloss.Style // a screen's own heading
	Body        lipgloss.Style
	Faint       lipgloss.Style
	Selected    lipgloss.Style // the current sidebar entry
	Key         lipgloss.Style // [Enter]
	KeyDesc     lipgloss.Style // run
	Code        lipgloss.Style // commands and paths
	Error       lipgloss.Style
	Warning     lipgloss.Style
	BorderColor color.Color
}

// New builds the theme for a dark or light background.
func New(isDark bool) Theme {
	ld := lipgloss.LightDark(isDark)
	t := Theme{
		IsDark: isDark,
		Accent: ld(lipgloss.Color("#5A3FC0"), lipgloss.Color("#A893FF")),
		Muted:  ld(lipgloss.Color("#6B6B6B"), lipgloss.Color("#8A8A8A")),
		Text:   ld(lipgloss.Color("#1F1F1F"), lipgloss.Color("#E6E6E6")),
		OK:     ld(lipgloss.Color("#1A7F37"), lipgloss.Color("#4AC26B")),
		Warn:   ld(lipgloss.Color("#9A6700"), lipgloss.Color("#E3B341")),
		Bad:    ld(lipgloss.Color("#CF222E"), lipgloss.Color("#FF7B72")),
	}
	t.BorderColor = t.Muted
	t.Title = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.Heading = lipgloss.NewStyle().Bold(true).Foreground(t.Text)
	t.Body = lipgloss.NewStyle().Foreground(t.Text)
	t.Faint = lipgloss.NewStyle().Foreground(t.Muted)
	t.Selected = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.Key = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.KeyDesc = lipgloss.NewStyle().Foreground(t.Muted)
	t.Code = lipgloss.NewStyle().Foreground(t.Text)
	t.Error = lipgloss.NewStyle().Bold(true).Foreground(t.Bad)
	t.Warning = lipgloss.NewStyle().Foreground(t.Warn)
	return t
}

// Mark renders the glyph for a state in its colour.
func (t Theme) Mark(s State) string {
	switch s {
	case Done, Skipped:
		return lipgloss.NewStyle().Foreground(t.OK).Render(GlyphDone)
	case Failed:
		return lipgloss.NewStyle().Foreground(t.Bad).Bold(true).Render(GlyphFailed)
	case Current:
		return lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render(GlyphCurrent)
	case Running:
		return lipgloss.NewStyle().Foreground(t.Accent).Render(GlyphRunning)
	}
	return lipgloss.NewStyle().Foreground(t.Muted).Render(GlyphPending)
}

// Key pairs a key with what it does, for the help line.
type Key struct{ Name, Does string }

// KeyHelp renders a help line such as "[Enter] run   [q] quit".
func (t Theme) KeyHelp(keys ...Key) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = t.Key.Render("["+k.Name+"]") + " " + t.KeyDesc.Render(k.Does)
	}
	return strings.Join(parts, "   ")
}

// Panel draws body inside a rounded border exactly width columns wide, with
// the title set into the top edge. Lines too long for the panel get cut.
func (t Theme) Panel(title, body string, width int) string {
	return t.box(title, body, width, t.BorderColor)
}

// AlertPanel draws a Panel with a warning-coloured border.
func (t Theme) AlertPanel(title, body string, width int) string {
	return t.box(title, body, width, t.Warn)
}

func (t Theme) box(title, body string, width int, border color.Color) string {
	inner := max(width-4, 1) // two border columns and a space each side
	edge := lipgloss.NewStyle().Foreground(border)
	title = ansi.Truncate(title, max(inner-2, 0), "…")
	fill := max(width-4-ansi.StringWidth(title), 0)
	var b strings.Builder
	b.WriteString(edge.Render("╭ ") + t.Heading.Render(title) + edge.Render(" "+strings.Repeat("─", fill)+"╮"))
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		line = ansi.Truncate(line, inner, "…")
		pad := inner - ansi.StringWidth(line)
		b.WriteString("\n" + edge.Render("│") + " " + line + strings.Repeat(" ", pad) + " " + edge.Render("│"))
	}
	b.WriteString("\n" + edge.Render("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	return b.String()
}
