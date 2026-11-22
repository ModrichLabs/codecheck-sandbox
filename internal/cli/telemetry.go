package cli

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
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
func withTelemetry(root *cobra.Command, env Env, opts *Options, newClient ClientFactory) {
	for _, top := range root.Commands() {
		wrapTree(top, top.Name(), env, opts, newClient)
	}
}

func wrapTree(c *cobra.Command, name string, env Env, opts *Options, newClient ClientFactory) {
	for _, child := range c.Commands() {
		wrapTree(child, name, env, opts, newClient)
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
		red := &system.Redactor{}
		log := slog.New(slog.DiscardHandler)
		if env.OpenLog != nil {
			if l, closer, lerr := env.OpenLog(opts.Debug, red); lerr == nil {
				log = l
				defer closer.Close()
			}
		}
		ctx := context.WithValue(base, sinkKey{}, sink)
		ctx = context.WithValue(ctx, logKey{}, logState{log: log, red: red})
		cmd.SetContext(ctx)
		log.Info("command started", "command", name, "version", buildinfo.Version)
		err := run(cmd, args)
		if err != nil {
			log.Error("command finished", "command", name, "outcome", string(outcomeOf(err)), "err", err.Error())
		} else {
			log.Info("command finished", "command", name, "outcome", string(outcomeOf(err)))
		}
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

type logKey struct{}

type logState struct {
	log *slog.Logger
	red *system.Redactor
}

// logFrom returns the running command's log, or one that discards.
func logFrom(ctx context.Context) *slog.Logger {
	if s, ok := ctx.Value(logKey{}).(logState); ok {
		return s.log
	}
	return slog.New(slog.DiscardHandler)
}

// redactSecret keeps secret out of the running command's log from now on.
func redactSecret(ctx context.Context, secret string) {
	if s, ok := ctx.Value(logKey{}).(logState); ok {
		s.red.Add(secret)
	}
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
