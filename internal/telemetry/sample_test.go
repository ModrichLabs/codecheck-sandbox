package telemetry

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The consent screen shows Payload's output, so it must match what Capture
// queues, field for field.
func TestPayloadMatchesWhatCaptureQueues(t *testing.T) {
	RegisterStepIDs(SampleStep)
	for _, e := range Sample("0.1.0-dev") {
		c, fsys, _ := newTestClient(t, "http://127.0.0.1:1", time.Second)
		c.Capture(e)
		b, err := fsys.ReadFile(filepath.Join("/state/telemetry", QueueFile))
		if err != nil {
			t.Fatalf("%s: %v", e.EventName(), err)
		}
		var q struct {
			Event      string         `json:"event"`
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(b), &q); err != nil {
			t.Fatal(err)
		}
		p, err := Payload(e)
		if err != nil {
			t.Fatalf("Payload(%s): %v", e.EventName(), err)
		}
		// Compare through JSON, as the queue does.
		var want map[string]any
		js, _ := json.Marshal(p)
		json.Unmarshal(js, &want)
		if q.Event != e.EventName() || !reflect.DeepEqual(q.Properties, want) {
			t.Errorf("%s: queued %v\npayload %v", e.EventName(), q.Properties, want)
		}
	}
}

func TestSampleCoversEveryEventWithRealValues(t *testing.T) {
	RegisterStepIDs(SampleStep)
	seen := map[string]bool{}
	for _, e := range Sample("0.1.0-dev") {
		seen[e.EventName()] = true
		p, err := Payload(e)
		if err != nil {
			t.Fatalf("%s: %v", e.EventName(), err)
		}
		for k, v := range p {
			if !Allowed(k) {
				t.Errorf("%s carries %q, which no event type declares", e.EventName(), k)
			}
			if s, ok := v.(string); ok && (s == "other" || s == "invalid" || s == "") {
				t.Errorf("%s.%s = %q: the sample should show a real value", e.EventName(), k, s)
			}
		}
	}
	for _, e := range AllEvents() {
		if !seen[e.EventName()] {
			t.Errorf("the sample lacks %s", e.EventName())
		}
	}
}

func TestAllowedRefusesOtherNames(t *testing.T) {
	for _, k := range []string{"$os", "path", "repo", "message", "api_key"} {
		if Allowed(k) {
			t.Errorf("Allowed(%q)", k)
		}
	}
	if !Allowed("cli_version") || !Allowed("$geoip_disable") {
		t.Error("Allowed refuses a real property")
	}
}

func TestSampleHoldsNoPlantedValue(t *testing.T) {
	var all strings.Builder
	for _, e := range Sample("0.1.0-dev") {
		p, _ := Payload(e)
		js, _ := json.Marshal(p)
		all.Write(js)
	}
	for _, v := range planted {
		if strings.Contains(all.String(), v) {
			t.Errorf("the sample holds %q", v)
		}
	}
}
