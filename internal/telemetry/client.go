package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/posthog/posthog-go"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// MaxQueueBytes caps the queue file; the oldest events go first.
const MaxQueueBytes = 1 << 20

// DefaultTimeout bounds Flush, so telemetry never holds a command up for
// longer.
const DefaultTimeout = 3 * time.Second

// Client queues events during a command and sends them at its end.
type Client interface {
	// Capture queues e locally. It never touches the network.
	Capture(e Event)
	// Flush sends the queue in one batch and clears it. It returns nothing:
	// failures go to the local log only, so they can never change a command's
	// outcome or exit code.
	Flush(ctx context.Context)
}

// Noop drops everything: telemetry off, or no project key.
type Noop struct{}

func (Noop) Capture(Event)         {}
func (Noop) Flush(context.Context) {}

// Options configure New.
type Options struct {
	Key, Host string
	Dir       string // the telemetry state folder
	FS        system.FS
	InstallID string
	Log       *slog.Logger // nil discards
	Timeout   time.Duration
	// MaxQueueBytes caps the queue; zero means the default MaxQueueBytes.
	MaxQueueBytes int
}

// New returns the PostHog client, or Noop when the key stays empty.
func New(o Options) Client {
	if o.Key == "" {
		return Noop{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxQueueBytes <= 0 {
		o.MaxQueueBytes = MaxQueueBytes
	}
	return &PostHog{o: o}
}

// PostHog queues to a file and sends through posthog-go.
type PostHog struct {
	o Options
}

type queued struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
	Timestamp  time.Time      `json:"timestamp"`
}

func (p *PostHog) queuePath() string { return filepath.Join(p.o.Dir, QueueFile) }

// Capture implements Client.
func (p *PostHog) Capture(e Event) {
	props, err := Payload(e)
	if err != nil {
		p.o.Log.Warn("telemetry: dropping an event", "event", e.EventName(), "err", err)
		return
	}
	line, err := json.Marshal(queued{Event: e.EventName(), Properties: props, Timestamp: time.Now().UTC()})
	if err != nil {
		p.o.Log.Warn("telemetry: dropping an event", "event", e.EventName(), "err", err)
		return
	}
	if err := p.append(append(line, '\n')); err != nil {
		p.o.Log.Warn("telemetry: queueing an event", "err", err)
	}
}

// append adds line to the queue and drops the oldest lines past the cap.
func (p *PostHog) append(line []byte) error {
	old, err := p.o.FS.ReadFile(p.queuePath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	buf := append(old, line...)
	for len(buf) > p.o.MaxQueueBytes {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			buf = nil
			break
		}
		buf = buf[i+1:]
	}
	if err := p.o.FS.MkdirAll(p.o.Dir, 0o700); err != nil {
		return err
	}
	return p.o.FS.WriteFile(p.queuePath(), buf, 0o600)
}

// Flush implements Client. It takes the queue first, so a failed send drops
// the events rather than repeating them later.
func (p *PostHog) Flush(ctx context.Context) {
	data, err := p.o.FS.ReadFile(p.queuePath())
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		p.o.Log.Warn("telemetry: reading the queue", "err", err)
		return
	}
	// Clear the queue before sending, and send nothing if that fails: the
	// next command would otherwise send the same events again.
	if err := p.o.FS.Remove(p.queuePath()); err != nil {
		p.o.Log.Warn("telemetry: clearing the queue; sending nothing", "err", err)
		return
	}
	var events []queued
	for line := range bytes.Lines(data) {
		var q queued
		if json.Unmarshal(line, &q) == nil && q.Event != "" {
			events = append(events, q)
		}
	}
	if len(events) == 0 {
		return
	}

	ph, err := posthog.NewWithConfig(p.o.Key, posthog.Config{
		Endpoint:           p.o.Host,
		DisableGeoIP:       posthog.Ptr(true),
		BatchSize:          len(events),
		Interval:           time.Hour, // Close sends the batch
		MaxRetries:         posthog.Ptr(0),
		ShutdownTimeout:    p.o.Timeout,
		BatchUploadTimeout: p.o.Timeout,
		Logger:             logAdapter{p.o.Log},
		Callback:           callback{p.o.Log},
		Transport:          stripTransport{next: http.DefaultTransport},
	})
	if err != nil {
		p.o.Log.Warn("telemetry: starting the client", "err", err)
		return
	}
	for _, q := range events {
		err := ph.Enqueue(posthog.Capture{
			DistinctId: p.o.InstallID,
			Event:      q.Event,
			Timestamp:  q.Timestamp,
			Properties: posthog.Properties(q.Properties),
		})
		if err != nil {
			p.o.Log.Warn("telemetry: queueing for send", "err", err)
		}
	}
	done := make(chan struct{})
	go func() {
		_ = ph.Close()
		close(done)
	}()
	timer := time.NewTimer(p.o.Timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		p.o.Log.Warn("telemetry: sending timed out; events dropped", "timeout", p.o.Timeout)
	case <-ctx.Done():
	}
}

// Payload builds the properties Capture queues for e: its fields, PostHog's
// exception shape for errors, and the two privacy flags. The consent screen
// shows its output, so what the user sees matches what gets sent.
func Payload(e Event) (map[string]any, error) {
	props, err := Properties(e)
	if err != nil {
		return nil, err
	}
	if ex, ok := e.(Exception); ok {
		props = exceptionShape(ex, props)
	}
	props["$process_person_profile"] = false
	props["$geoip_disable"] = true
	return props, nil
}

// exceptionShape moves the stack into PostHog's $exception_list format.
func exceptionShape(ex Exception, props map[string]any) map[string]any {
	frames := props["stack"]
	delete(props, "stack")
	props["$exception_list"] = []map[string]any{{
		"type":      props["kind"],
		"value":     props["kind"],
		"mechanism": map[string]any{"handled": true, "synthetic": false},
		"stacktrace": map[string]any{
			"type":   "raw",
			"frames": frames,
		},
	}}
	return props
}

// sdkProperties lists what posthog-go may add on its own.
var sdkProperties = map[string]bool{
	"$lib": true, "$lib_version": true, "$geoip_disable": true,
	"$process_person_profile": true, "$is_server": true, "$exception_list": true,
}

// allowedProperty reports whether k may leave the machine: a prop tag of a
// known event, or one of the SDK's own fields above.
func allowedProperty(k string) bool {
	if sdkProperties[k] {
		return true
	}
	for _, e := range AllEvents() {
		props, _ := Properties(e)
		if _, ok := props[k]; ok {
			return true
		}
	}
	return false
}

// stripTransport sits under posthog-go as its HTTP transport, the last point
// before bytes leave the machine. It lets through only a POST to /batch/,
// decodes the batch, removes every property not on the allowlist (posthog-go
// adds $os, $os_version, $os_distro and $go_version after any hook could see
// them) and sends the result. Anything it cannot read, it refuses to send.
type stripTransport struct{ next http.RoundTripper }

var errNotSent = errors.New("telemetry: refused to send a request it could not check")

func (s stripTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.URL.Path != "/batch/" || r.Body == nil {
		return nil, errNotSent
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, errNotSent
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errNotSent
	}
	batch, ok := payload["batch"].([]any)
	if !ok {
		return nil, errNotSent
	}
	for _, item := range batch {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, errNotSent
		}
		props, _ := msg["properties"].(map[string]any)
		for k := range props {
			if !allowedProperty(k) {
				delete(props, k)
			}
		}
	}
	clean, err := json.Marshal(payload)
	if err != nil {
		return nil, errNotSent
	}
	out := r.Clone(r.Context())
	out.Body = io.NopCloser(bytes.NewReader(clean))
	out.ContentLength = int64(len(clean))
	out.Header.Del("Content-Encoding")
	return s.next.RoundTrip(out)
}

// logAdapter sends posthog-go's own messages to the local log.
type logAdapter struct{ log *slog.Logger }

func (l logAdapter) Debugf(f string, a ...any) { l.log.Debug("telemetry: " + fmt.Sprintf(f, a...)) }
func (l logAdapter) Logf(f string, a ...any)   { l.log.Debug("telemetry: " + fmt.Sprintf(f, a...)) }
func (l logAdapter) Warnf(f string, a ...any)  { l.log.Warn("telemetry: " + fmt.Sprintf(f, a...)) }
func (l logAdapter) Errorf(f string, a ...any) { l.log.Warn("telemetry: " + fmt.Sprintf(f, a...)) }

// callback logs batch failures.
type callback struct{ log *slog.Logger }

func (callback) Success(posthog.APIMessage) {}
func (c callback) Failure(_ posthog.APIMessage, err error) {
	c.log.Warn("telemetry: sending failed; events dropped", "err", err)
}
