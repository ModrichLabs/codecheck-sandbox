package cli

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

// ClientFactory builds the telemetry client once the global flags have
// parsed.
type ClientFactory func(Options) telemetry.Client

// withTelemetry wraps every runnable command in the tree so it records
// command_run under its top-level name (and an $exception on failure) and
// flushes once at the end. The client gets built after the command runs, so
// `telemetry off` takes effect before anything could send. Flush returns
// nothing, so telemetry can never change a command's result.
func withTelemetry(root *cobra.Command, opts *Options, newClient ClientFactory) {
	for _, top := range root.Commands() {
		wrapTree(top, top.Name(), opts, newClient)
	}
}

func wrapTree(c *cobra.Command, name string, opts *Options, newClient ClientFactory) {
	for _, child := range c.Commands() {
		wrapTree(child, name, opts, newClient)
	}
	run := c.RunE
	if run == nil {
		return
	}
	c.RunE = func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		sink := &eventSink{}
		base := cmd.Context()
		if base == nil {
			base = context.Background()
		}
		cmd.SetContext(context.WithValue(base, sinkKey{}, sink))
		err := run(cmd, args)
		client := newClient(*opts)
		client.Capture(telemetry.CommandRun{
			Command:    telemetry.Command(name),
			Outcome:    outcomeOf(err),
			Duration:   telemetry.DurationBandOf(time.Since(start)),
			CLIVersion: telemetry.Version(buildinfo.Version),
		})
		for _, e := range sink.take() {
			client.Capture(e)
		}
		if err != nil && !errors.Is(err, step.ErrDeclined) {
			client.Capture(telemetry.NewException(err, ""))
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		client.Flush(ctx)
		return err
	}
}

// eventSink collects the events a command records while it runs. The
// wrapper captures them after the command, once consent is known.
type eventSink struct {
	mu     sync.Mutex
	events []telemetry.Event
}

type sinkKey struct{}

func (s *eventSink) take() []telemetry.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

// recordEvent hands e to the running command's sink, if any.
func recordEvent(ctx context.Context, e telemetry.Event) {
	if s, ok := ctx.Value(sinkKey{}).(*eventSink); ok {
		s.mu.Lock()
		s.events = append(s.events, e)
		s.mu.Unlock()
	}
}

func outcomeOf(err error) telemetry.Outcome {
	switch {
	case err == nil:
		return telemetry.OutcomeOK
	case errors.Is(err, step.ErrDeclined):
		return telemetry.OutcomeDeclined
	}
	return telemetry.OutcomeFailed
}
