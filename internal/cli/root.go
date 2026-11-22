// Package cli defines the codecheck-sandbox command tree.
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
)

// ErrNotImplemented marks a command that exists in the tree but has no
// behaviour yet.
var ErrNotImplemented = errors.New("not implemented")

// Options holds the global flags shared by every subcommand.
type Options struct {
	Yes         bool
	DryRun      bool
	Plain       bool
	Debug       bool
	NoTelemetry bool
}

// NewRootCommand builds a fresh command tree for the real process, so tests
// never share state.
func NewRootCommand() *cobra.Command {
	env := ProcessEnv()
	return NewRootCommandEnv(env, realTelemetry(env))
}

// NewRootCommandWith builds the command tree with the given telemetry client
// factory; tests pass a fake.
func NewRootCommandWith(newClient ClientFactory) *cobra.Command {
	return NewRootCommandEnv(ProcessEnv(), newClient)
}

// NewRootCommandEnv builds the command tree against env, with the given
// telemetry client factory.
func NewRootCommandEnv(env Env, newClient ClientFactory) *cobra.Command {
	var opts Options

	root := &cobra.Command{
		Use:   buildinfo.Name,
		Short: "Run CodeCheck security reviews inside jailed Firecracker microVMs",
		Long: buildinfo.Name + " sets up a Linux machine with KVM to run CodeCheck reviews\n" +
			"inside jailed Firecracker microVMs, checks that setup, and runs reviews.",
		Version:       buildinfo.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate(versionLine())
	root.CompletionOptions.DisableDefaultCmd = true
	// Defining --version here stops cobra adding a -v shorthand, which a
	// later flag may need.
	root.Flags().Bool("version", false, "print the version")

	f := root.PersistentFlags()
	f.BoolVar(&opts.Yes, "yes", false, "run root steps without asking (reads the API key from CODECHECK_API_KEY)")
	f.BoolVar(&opts.DryRun, "dry-run", false, "print what would change, and change nothing")
	f.BoolVar(&opts.Plain, "plain", false, "print plain lines instead of the full-screen interface")
	f.BoolVar(&opts.Debug, "debug", false, "log more detail, and echo the log to the terminal")
	f.BoolVar(&opts.NoTelemetry, "no-telemetry", false, "send no usage or error reports, whatever the settings say")

	root.AddCommand(
		stub("setup", "Guide this machine through setup, step by step"),
		stub("doctor", "Check every part of the setup and report problems"),
		stub("selftest", "Boot a VM and try each way out of the sandbox"),
		stub("canary", "Run a small paid review of a known-vulnerable sample"),
		newReviewCommand(env, &opts),
		stub("rebuild", "Rebuild the guest image, for example for a new CodeCheck commit"),
		stub("uninstall", "Remove everything setup installed"),
		newTelemetryCommand(env, &opts),
		hidden(newProxyCommand()),
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, err := fmt.Fprint(cmd.OutOrStdout(), versionLine())
				return err
			},
		},
	)
	withTelemetry(root, &opts, newClient)
	return root
}

func versionLine() string {
	return buildinfo.Name + " " + buildinfo.Version + "\n"
}

func stub(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%s: %w", name, ErrNotImplemented)
		},
	}
}

func hidden(c *cobra.Command) *cobra.Command {
	c.Hidden = true
	return c
}
