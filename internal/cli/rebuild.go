package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// imageStep names the guest image step rebuild forces.
const imageStep = "guest-image"

var fullCommit = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func newRebuildCommand(env Env, opts *Options) *cobra.Command {
	var commit string
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild the guest image, for example for a new CodeCheck commit",
		Long: "Builds the guest image again, even when the current one matches its\n" +
			"inputs. --commit picks another CodeCheck commit (a full hash); config.toml\n" +
			"keeps it only once the build succeeds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if commit != "" && !fullCommit.MatchString(commit) {
				return fmt.Errorf("--commit %q: want a full commit hash (40 or 64 hex characters)", commit)
			}
			if env.EUID == 0 {
				return errors.New("run rebuild as your normal user; it asks for root only where it needs it")
			}
			cfg, _, err := env.LoadConfig()
			if err != nil {
				return err
			}
			if commit == "" {
				commit = cfg.CodeCheckCommit
			}
			host, err := env.OpenSetup(*opts)
			if err != nil {
				return err
			}
			var mu sync.Mutex
			var created []step.Item
			d := host.Deps
			d.CodeCheckCommit = commit
			d.Record = func(it step.Item) { mu.Lock(); created = append(created, it); mu.Unlock() }
			all, err := host.Steps(d)
			if err != nil {
				return err
			}
			var steps []step.Step
			for _, s := range all {
				if s.ID() == imageStep {
					steps = append(steps, &forced{Step: s})
				}
			}
			if len(steps) == 0 {
				return errors.New("rebuild: no guest image step")
			}
			unlock, err := host.Lock(ctx)
			if err != nil {
				return err
			}
			defer unlock()
			runErr := runEngine(ctx, env, opts, host, steps, step.Options{DryRun: opts.DryRun, AssumeYes: opts.Yes}, cmd.OutOrStdout())
			if !opts.DryRun && len(created) > 0 {
				if err := saveManifest(ctx, host, created); err != nil {
					runErr = errors.Join(runErr, err)
				}
			}
			if runErr != nil || opts.DryRun {
				return runErr
			}
			if cfg.CodeCheckCommit != commit {
				cfg.CodeCheckCommit = commit
				if err := env.SaveConfig(cfg); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Rebuilt the guest image with CodeCheck "+commit[:12]+".")
			return nil
		},
	}
	cmd.Flags().StringVar(&commit, "commit", "", "build this CodeCheck commit (a full hash) instead of the current one")
	return cmd
}

// forced makes a step's first check fail, so the engine applies it even
// when its result already holds; later checks (the one after Apply) run for
// real.
type forced struct {
	step.Step
	asked bool
}

func (f *forced) Check(ctx context.Context) (step.Status, error) {
	if !f.asked {
		f.asked = true
		return step.Status{Detail: "rebuild asked for a fresh build"}, nil
	}
	return f.Step.Check(ctx)
}
