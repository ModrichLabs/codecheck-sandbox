package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

const testID = "0b6f5a3e-8c1d-4f2a-9e7b-1c2d3e4f5a6b"

// capturedBatch records what a fake PostHog received.
type capturedBatch struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capturedBatch) events(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, b := range c.bodies {
		var payload struct {
			Batch []map[string]any `json:"batch"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Fatalf("server got a body that is not a batch: %v\n%s", err, b)
		}
		out = append(out, payload.Batch...)
	}
	return out
}

func fakePostHog(t *testing.T, status int) (*httptest.Server, *capturedBatch) {
	t.Helper()
	got := &capturedBatch{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.bodies = append(got.bodies, body)
		got.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestClient(t *testing.T, host string, timeout time.Duration) (Client, *system.MemFS, *bytes.Buffer) {
	t.Helper()
	fsys := system.NewMemFS()
	var logBuf bytes.Buffer
	c := New(Options{
		Key:       "phc_testkeytestkeytestkeytestkey",
		Host:      host,
		Dir:       "/state/telemetry",
		FS:        fsys,
		InstallID: testID,
		Log:       slog.New(slog.NewTextHandler(&logBuf, nil)),
		Timeout:   timeout,
	})
	return c, fsys, &logBuf
}

func TestFlushSendsTheBatchWithPrivacyProperties(t *testing.T) {
	RegisterStepIDs("jail-user")
	srv, got := fakePostHog(t, http.StatusOK)
	c, fsys, _ := newTestClient(t, srv.URL, 3*time.Second)

	c.Capture(CommandRun{Command: "setup", Outcome: OutcomeOK, Duration: DurationBandOf(time.Minute), CLIVersion: "0.1.0-dev"})
	c.Capture(StepFinished{Step: "jail-user", Outcome: OutcomeFailed, Duration: DurationBandOf(time.Second)})
	c.Capture(NewException(context.Canceled, "jail-user"))
	c.Flush(context.Background())

	events := got.events(t)
	if len(events) != 3 {
		t.Fatalf("server got %d events, want 3", len(events))
	}
	names := []string{"command_run", "step_finished", "$exception"}
	for i, e := range events {
		if e["event"] != names[i] {
			t.Errorf("event %d = %v, want %s", i, e["event"], names[i])
		}
		if e["distinct_id"] != testID {
			t.Errorf("distinct_id = %v, want the install id", e["distinct_id"])
		}
		props := e["properties"].(map[string]any)
		if props["$process_person_profile"] != false || props["$geoip_disable"] != true {
			t.Errorf("%s: privacy properties = %v / %v", names[i], props["$process_person_profile"], props["$geoip_disable"])
		}
		for k := range props {
			if !allowedProperty(k) {
				t.Errorf("%s carries unexpected property %q", names[i], k)
			}
		}
	}
	cmd := events[0]["properties"].(map[string]any)
	if cmd["command"] != "setup" || cmd["cli_version"] != "0.1.0-dev" {
		t.Errorf("command_run properties = %v", cmd)
	}
	ex := events[2]["properties"].(map[string]any)
	list, ok := ex["$exception_list"].([]any)
	if !ok || len(list) != 1 || list[0].(map[string]any)["type"] != "canceled" {
		t.Errorf("$exception_list = %v", ex["$exception_list"])
	}
	if _, err := fsys.Stat(filepath.Join("/state/telemetry", QueueFile)); err == nil {
		t.Error("the queue survived a flush")
	}
}

func TestFlushNeverOutlastsItsDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test ends
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	c, _, _ := newTestClient(t, srv.URL, 300*time.Millisecond)

	c.Capture(CommandRun{Command: "doctor", Outcome: OutcomeOK})
	start := time.Now()
	c.Flush(context.Background())
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Flush took %v against a hanging server, want about 300ms", took)
	}
}

func TestFlushSwallowsServerErrorsIntoTheLog(t *testing.T) {
	srv, got := fakePostHog(t, http.StatusInternalServerError)
	c, _, logBuf := newTestClient(t, srv.URL, 2*time.Second)
	c.Capture(CommandRun{Command: "review", Outcome: OutcomeOK})
	c.Flush(context.Background()) // returns nothing, so it cannot change an exit code
	if len(got.events(t)) == 0 {
		t.Fatal("server never got the batch")
	}
	if !strings.Contains(logBuf.String(), "telemetry") {
		t.Errorf("the failure never reached the local log:\n%s", logBuf.String())
	}
}

func TestCaptureNeverTouchesTheNetwork(t *testing.T) {
	srv, got := fakePostHog(t, http.StatusOK)
	c, fsys, _ := newTestClient(t, srv.URL, time.Second)
	c.Capture(CommandRun{Command: "setup", Outcome: OutcomeOK})
	if n := len(got.events(t)); n != 0 {
		t.Errorf("Capture sent %d events before Flush", n)
	}
	q, err := fsys.ReadFile(filepath.Join("/state/telemetry", QueueFile))
	if err != nil || !strings.Contains(string(q), `"command_run"`) {
		t.Errorf("queue = %q, %v; want the event queued", q, err)
	}
}

func TestQueueStaysUnderItsCap(t *testing.T) {
	const capBytes = 4096
	fsys := system.NewMemFS()
	c := New(Options{Key: "phc_testkeytestkeytestkeytestkey", Host: "http://127.0.0.1:1", Dir: "/state/telemetry", FS: fsys, InstallID: testID, MaxQueueBytes: capBytes})
	for i := range 200 {
		c.Capture(CommandRun{Command: "doctor", Outcome: OutcomeOK, CLIVersion: Version("0.1." + strconv.Itoa(i))})
	}
	q, _ := fsys.ReadFile(filepath.Join("/state/telemetry", QueueFile))
	if len(q) > capBytes {
		t.Errorf("queue holds %d bytes, over the %d cap", len(q), capBytes)
	}
	lines := strings.Split(strings.TrimSpace(string(q)), "\n")
	if len(lines) >= 200 {
		t.Fatalf("queue kept all %d events; the cap never applied", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], `"0.1.199"`) {
		t.Errorf("the newest event got dropped instead of the oldest: %s", lines[len(lines)-1])
	}
	if strings.Contains(lines[0], `"0.1.0"`) {
		t.Errorf("the oldest event survived: %s", lines[0])
	}
}

func TestTransportStripsUnknownProperties(t *testing.T) {
	var seen []byte
	next := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})
	body := `{"api_key":"phc_x","batch":[{"event":"command_run","distinct_id":"id","properties":{"command":"setup","$os_version":"6.17.9-arch1-1","$hostname":"alice-laptop"}}]}`
	req := httptest.NewRequest(http.MethodPost, "https://us.i.posthog.com/batch/", strings.NewReader(body))
	if _, err := (stripTransport{next: next}).RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	for _, bad := range []string{"$os_version", "6.17.9", "alice-laptop"} {
		if strings.Contains(string(seen), bad) {
			t.Errorf("sent body still holds %q: %s", bad, seen)
		}
	}
	if !strings.Contains(string(seen), `"command":"setup"`) {
		t.Errorf("sent body lost an allowed property: %s", seen)
	}
}

func TestTransportFailsClosedOnAnUnreadableBody(t *testing.T) {
	called := false
	next := roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	req := httptest.NewRequest(http.MethodPost, "https://us.i.posthog.com/batch/", strings.NewReader("\x1f\x8b gzip?"))
	if _, err := (stripTransport{next: next}).RoundTrip(req); err == nil || called {
		t.Errorf("err = %v, sent = %v; an unreadable body must never go out", err, called)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEmptyKeyFallsBackToNoop(t *testing.T) {
	c := New(Options{Key: "", Host: "https://us.i.posthog.com", Dir: "/x", FS: system.NewMemFS(), InstallID: testID})
	if _, ok := c.(Noop); !ok {
		t.Errorf("New with an empty key = %T, want Noop", c)
	}
	c.Capture(CommandRun{})
	c.Flush(context.Background())
}
