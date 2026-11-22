package cli

import (
	"context"
	"errors"
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
		err := run(cmd, args)
		client := newClient(*opts)
		client.Capture(telemetry.CommandRun{
			Command:    telemetry.Command(name),
			Outcome:    outcomeOf(err),
			Duration:   telemetry.DurationBandOf(time.Since(start)),
			CLIVersion: telemetry.Version(buildinfo.Version),
		})
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

func outcomeOf(err error) telemetry.Outcome {
	switch {
	case err == nil:
		return telemetry.OutcomeOK
	case errors.Is(err, step.ErrDeclined):
		return telemetry.OutcomeDeclined
	}
	return telemetry.OutcomeFailed
}
