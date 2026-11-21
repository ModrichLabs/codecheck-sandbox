package tui

import (
	"encoding/json"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// consentText explains the choice above the sample (spec §11).
const consentText = "Help improve codecheck-sandbox by sending anonymous counts: which commands ran, how each " +
	"step ended, banded durations and costs, your distro and firewall type, and the kind of any error. " +
	"It never sends code, file paths, repository names, prompts, findings, error messages, your API key, " +
	"hostnames or IP addresses. Events go to PostHog (US) under a random id made on this machine. " +
	"Change your mind any time with codecheck-sandbox telemetry on or off. Below, exactly what gets sent:"

// Consent asks once whether to send telemetry. It starts on No, and only a
// deliberate y or → then Enter turns it on. Ctrl+C, q or Esc leaves with
// no answer (Aborted), so setup can stop.
type Consent struct {
	th      theme.Theme
	vp      viewport.Model
	sample  string
	yes     bool
	done    bool
	aborted bool
	width   int
	height  int
}

// NewConsent builds the consent screen with a sample rendered from the real
// event types.
func NewConsent(th theme.Theme, cliVersion string) *Consent {
	c := &Consent{th: th, sample: SamplePayload(cliVersion), width: 100, height: 30}
	c.layout()
	return c
}

// SamplePayload renders one event of each type exactly as Capture would
// queue it, plus the envelope PostHog receives.
func SamplePayload(cliVersion string) string {
	var b strings.Builder
	for _, e := range telemetry.Sample(cliVersion) {
		props, err := telemetry.Payload(e)
		if err != nil {
			continue // a sample event that can't encode would never get sent either
		}
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false) // show <10s as sent, not <10s
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"event":       e.EventName(),
			"distinct_id": "<a random id made on this machine>",
			"properties":  props,
		})
	}
	return b.String()
}

// Choice reports the answer: true only for a clear yes.
func (c *Consent) Choice() bool { return c.yes }

// Aborted reports a screen left with Ctrl+C, q or Esc: no answer given.
func (c *Consent) Aborted() bool { return c.aborted }

// Done reports an answered screen.
func (c *Consent) Done() bool { return c.done }

// Init implements tea.Model.
func (c *Consent) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (c *Consent) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = msg.Width, msg.Height
		c.layout()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y", "Y":
			c.yes = true
		case "n", "N":
			c.yes = false
		case "left", "h":
			c.yes = false
		case "right", "l":
			c.yes = true
		case "tab":
			c.yes = !c.yes
		case "enter":
			c.done = true
			return c, tea.Quit
		case "ctrl+c", "q", "esc":
			// Leaving answers nothing, and telemetry stays off.
			c.done, c.aborted, c.yes = true, true, false
			return c, tea.Quit
		default:
			var cmd tea.Cmd
			c.vp, cmd = c.vp.Update(msg)
			return c, cmd
		}
	}
	return c, nil
}

func (c *Consent) textWidth() int { return max(c.width-4, 20) }

func (c *Consent) intro() string {
	return lipgloss.NewStyle().Width(c.textWidth()).Render(consentText)
}

// layout gives the sample whatever rows the rest leaves: the frame (2), the
// heading and its gap (2), the text, the sample's border (2), and the gaps,
// buttons and help (4).
func (c *Consent) layout() {
	used := 2 + 2 + lipgloss.Height(c.intro()) + 2 + 4
	c.vp = viewport.New(viewport.WithWidth(c.textWidth()-4), viewport.WithHeight(max(c.height-used, 3)))
	c.vp.SetContent(strings.TrimRight(c.sample, "\n"))
}

// View implements tea.Model.
func (c *Consent) View() tea.View {
	button := func(label string, on bool) string {
		// The dot shows the choice even where colour doesn't reach.
		if on {
			return c.th.Key.Reverse(true).Padding(0, 1).Render("● " + label)
		}
		return c.th.Faint.Padding(0, 1).Render("○ " + label)
	}
	body := strings.Join([]string{
		c.th.Heading.Render("Anonymous usage and error reporting"),
		"",
		c.intro(),
		c.th.Panel("Sample payload", c.vp.View(), c.textWidth()),
		"",
		button("No, keep it off", !c.yes) + "  " + button("Yes, send anonymous counts", c.yes),
		"",
		c.th.KeyHelp(theme.Key{Name: "←/→", Does: "choose"}, theme.Key{Name: "↑/↓", Does: "scroll"},
			theme.Key{Name: "Enter", Does: "confirm"}, theme.Key{Name: "q", Does: "quit"}),
	}, "\n")
	v := tea.NewView(c.th.Panel("CodeCheck Sandbox setup", body, c.width))
	v.AltScreen = true
	return v
}

func stripANSI(s string) string { return ansi.Strip(s) }
