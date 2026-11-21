package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

func init() { telemetry.RegisterStepIDs(telemetry.SampleStep) }

func newConsentAt(w, h int) *Consent {
	c := NewConsent(theme.New(true), "0.1.0-dev")
	c.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return c
}

func send(c *Consent, keys ...string) {
	for _, k := range keys {
		c.Update(keyMsg(k))
	}
}

func TestConsentDefaultsToNo(t *testing.T) {
	c := newConsentAt(100, 30)
	if c.Choice() {
		t.Fatal("consent starts as yes")
	}
	send(c, "enter")
	if !c.Done() || c.Choice() {
		t.Errorf("Enter straight away: done %v, choice %v", c.Done(), c.Choice())
	}
}

func TestConsentNeedsAClearYes(t *testing.T) {
	c := newConsentAt(100, 30)
	send(c, "y")
	if c.Done() || !c.Choice() {
		t.Error("y should pick yes without finishing")
	}
	send(c, "n")
	if c.Choice() {
		t.Error("n should pick no")
	}
	send(c, "right", "enter")
	if !c.Done() || !c.Choice() {
		t.Errorf("→ then Enter: done %v, choice %v", c.Done(), c.Choice())
	}
}

func TestConsentScreen(t *testing.T) {
	c := newConsentAt(100, 30)
	s := strings.TrimRight(stripView(c), "\n")
	checkSize(t, s)
	golden(t, "consent", s)
	for _, want := range []string{"command_run", "\"distinct_id\"", "No", "Yes", "telemetry on"} {
		if !strings.Contains(s, want) {
			t.Errorf("consent screen lacks %q", want)
		}
	}
}

func stripView(c *Consent) string { return stripANSI(c.View().Content) }

// The sample must hold nothing the event types don't declare: the envelope
// fields, the event names and the properties.
func TestConsentSampleHoldsOnlyEventFields(t *testing.T) {
	sample := SamplePayload("0.1.0-dev")
	var names []string
	for _, e := range telemetry.AllEvents() {
		names = append(names, e.EventName())
	}
	dec := json.NewDecoder(strings.NewReader(sample))
	n := 0
	for dec.More() {
		var ev map[string]any
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("the sample isn't a run of JSON objects: %v\n%s", err, sample)
		}
		n++
		for k := range ev {
			if k != "event" && k != "distinct_id" && k != "properties" {
				t.Errorf("envelope field %q", k)
			}
		}
		if !slices.Contains(names, ev["event"].(string)) {
			t.Errorf("unknown event %v", ev["event"])
		}
		for k := range ev["properties"].(map[string]any) {
			if !telemetry.Allowed(k) {
				t.Errorf("%v carries %q", ev["event"], k)
			}
		}
	}
	if n != len(names) {
		t.Errorf("the sample shows %d events, want one of each of %d", n, len(names))
	}
}
