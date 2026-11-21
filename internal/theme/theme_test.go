package theme

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// render passes s through a terminal of the given colour profile.
func render(t *testing.T, p colorprofile.Profile, s string) string {
	t.Helper()
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: p}
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestMarksRenderAtTheASCIIProfile(t *testing.T) {
	th := New(true)
	for state, want := range map[State]string{
		Done: "✓", Failed: "✗", Pending: "·", Current: "▸", Running: "⣾", Skipped: "✓",
	} {
		got := render(t, colorprofile.ASCII, th.Mark(state))
		if strings.Contains(got, "38;") || strings.Contains(got, "48;") {
			t.Errorf("%v: colour codes survived the ASCII profile: %q", state, got)
		}
		if ansi.Strip(got) != want {
			t.Errorf("%v renders as %q, want %q", state, ansi.Strip(got), want)
		}
	}
}

func TestMarksCarryColourOnAColourTerminal(t *testing.T) {
	got := render(t, colorprofile.TrueColor, New(true).Mark(Failed))
	if !strings.Contains(got, "38;2;") {
		t.Errorf("a failed mark lost its colour: %q", got)
	}
}

func TestKeyHelp(t *testing.T) {
	got := ansi.Strip(New(false).KeyHelp(Key{"Enter", "run"}, Key{"d", "show files"}, Key{"q", "quit"}))
	if got != "[Enter] run   [d] show files   [q] quit" {
		t.Errorf("KeyHelp = %q", got)
	}
}

func TestPalettesSuitTheBackground(t *testing.T) {
	light, dark := New(false), New(true)
	if light.Accent == dark.Accent || light.Muted == dark.Muted {
		t.Error("light and dark terminals got the same accent or muted colour")
	}
}

func TestPanelFitsItsWidth(t *testing.T) {
	out := New(true).Panel("Needs root: review before running", "ip link set cc-tap0 up", 40)
	for _, line := range strings.Split(ansi.Strip(out), "\n") {
		if w := ansi.StringWidth(line); w != 40 {
			t.Errorf("panel line %q spans %d columns, want 40", line, w)
		}
	}
	if !strings.Contains(ansi.Strip(out), "Needs root") {
		t.Errorf("panel lost its title:\n%s", ansi.Strip(out))
	}
}
