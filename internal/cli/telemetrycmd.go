package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

// realTelemetry builds the client the consent rules allow (spec §11): the
// PostHog client only when Resolve says On, Noop otherwise. Anything that
// goes wrong on the way, such as an unreadable config, means off; telemetry
// never fails a command.
func realTelemetry(env Env) ClientFactory {
	return func(o Options) telemetry.Client {
		if consent(env, o) != telemetry.On {
			return telemetry.Noop{}
		}
		id, err := telemetry.EnsureInstallID(env.FS, env.TelemetryDir())
		if err != nil {
			return telemetry.Noop{}
		}
		return telemetry.New(telemetry.Options{
			Key: pins.PostHogKey, Host: pins.PostHogHost,
			Dir: env.TelemetryDir(), FS: env.FS, InstallID: id,
			Timeout: telemetry.DefaultTimeout,
		})
	}
}

// consent resolves the telemetry decision; Ask counts as off everywhere but
// in setup's consent screen.
func consent(env Env, o Options) telemetry.Decision {
	c, _, err := env.LoadConfig()
	if err != nil {
		return telemetry.Off
	}
	d := telemetry.Resolve(telemetry.ConsentInputs{
		DoNotTrack: env.Environ["DO_NOT_TRACK"], NoTelemetry: o.NoTelemetry,
		AssumeYes: o.Yes, TTY: env.TTY, Config: c.Telemetry,
	})
	if d == telemetry.Ask {
		return telemetry.Off
	}
	return d
}

func newTelemetryCommand(env Env, opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Show or change anonymous usage and error reporting",
		Long: "Anonymous usage and error reporting stays off unless you turn it on.\n" +
			"docs/usage/privacy-and-telemetry.md lists exactly what gets sent.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return telemetryStatus(cmd, env, opts) },
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "status", Short: "Show whether reporting runs, and why", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return telemetryStatus(cmd, env, opts) },
		},
		&cobra.Command{
			Use: "on", Short: "Send anonymous usage and error reports", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return setTelemetry(cmd, env, opts, true) },
		},
		&cobra.Command{
			Use: "off", Short: "Stop reporting, and delete the install id and any queued events", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return setTelemetry(cmd, env, opts, false) },
		},
	)
	return cmd
}

func setTelemetry(cmd *cobra.Command, env Env, opts *Options, on bool) error {
	c, _, err := env.LoadConfig()
	if err != nil {
		return err
	}
	c.Telemetry = &on
	if err := env.SaveConfig(c); err != nil {
		return err
	}
	if on {
		if _, err := telemetry.EnsureInstallID(env.FS, env.TelemetryDir()); err != nil {
			return err
		}
		switch {
		case env.Environ["DO_NOT_TRACK"] != "" && env.Environ["DO_NOT_TRACK"] != "0":
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Telemetry stored as on, but DO_NOT_TRACK keeps it off while set.")
		case opts.NoTelemetry:
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Telemetry stored as on; runs with --no-telemetry still send nothing.")
		default:
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Telemetry on: anonymous counts go to PostHog (US) under a random id made on this machine.")
		}
		return err
	}
	if err := telemetry.Forget(env.FS, env.TelemetryDir()); err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Telemetry off: the install id and any queued events got deleted.")
	return err
}

func telemetryStatus(cmd *cobra.Command, env Env, opts *Options) error {
	c, _, err := env.LoadConfig()
	var line string
	switch {
	case err != nil:
		line = "off: config.toml didn't load (" + err.Error() + ")"
	case env.Environ["DO_NOT_TRACK"] != "" && env.Environ["DO_NOT_TRACK"] != "0":
		line = "off: DO_NOT_TRACK asks for none"
	case opts.NoTelemetry:
		line = "off: --no-telemetry"
	case c.Telemetry == nil:
		line = "off: not set yet; setup asks, or run codecheck-sandbox telemetry on"
	case *c.Telemetry:
		line = "on (stored in config.toml)"
	default:
		line = "off: turned off in config.toml"
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Telemetry "+line)
	return err
}
