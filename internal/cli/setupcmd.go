package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
	"github.com/ModrichLabs/codecheck-sandbox/internal/distro"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/platform"
	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/setup"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// SetupHost holds what setup, doctor and uninstall need from the machine;
// tests replace every part.
type SetupHost struct {
	// Deps carries the host facts; the command fills in Record, APIKey,
	// KernelFile and CodeCheckCommit.
	Deps setup.Deps
	// Steps builds the step list (setup.Steps).
	Steps func(setup.Deps) ([]step.Step, error)
	// LoadManifest returns the installed manifest, or nil when none exists.
	LoadManifest func() (*step.Manifest, error)
	// SaveManifest writes it, root-owned.
	SaveManifest func(context.Context, *step.Manifest) error
	// Lock takes the footprint lock; it returns a no-op before the first
	// install has created the lock file.
	Lock func(context.Context) (unlock func(), err error)
	// Handover runs the password prompt on the full-screen interface;
	// Elevate does it in plain mode. Either may stay nil.
	Handover *tui.Handover
	Elevate  step.ElevateFunc
}

// lockWait bounds how long setup waits for a running review to finish.
const lockWait = 5 * time.Second

func newSetupHost(env Env, _ Options) (*SetupHost, error) {
	ctx := context.Background()
	tool, err := system.DetectTool(ctx, system.ExecRunner{}, exec.LookPath)
	if err != nil {
		return nil, err
	}
	priv := system.PrivRunner{Tool: tool, Inner: system.ExecRunner{}}
	family := distro.Family("")
	if rel, err := distro.Detect(system.OSFS{}); err == nil {
		family = rel.Family()
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cacheBase := env.Environ["XDG_CACHE_HOME"]
	if !filepath.IsAbs(cacheBase) {
		cacheBase = filepath.Join(env.Home, ".cache")
	}
	return &SetupHost{
		Deps: setup.Deps{
			Platform: platform.DefaultEnv(), Family: family, Root: priv, User: env.Environ["USER"],
			FS: system.OSFS{}, Look: system.LookTool,
			GuestInit: filepath.Join(filepath.Dir(exe), "cc-guestinit"),
			Cache:     filepath.Join(cacheBase, "codecheck-sandbox", "build"),
			Launcher:  launch.Client{Priv: priv, Console: io.Discard},
			Now:       time.Now,
		},
		Steps: setup.Steps,
		LoadManifest: func() (*step.Manifest, error) {
			m, err := step.LoadManifest(system.OSFS{}, step.ManifestPath)
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return m, err
		},
		SaveManifest: func(ctx context.Context, m *step.Manifest) error { return m.Save(ctx, priv, step.ManifestPath) },
		Lock: func(ctx context.Context) (func(), error) {
			l, err := step.Lock(ctx, step.LockPath, lockWait)
			if errors.Is(err, fs.ErrNotExist) {
				return func() {}, nil // the first install creates the lock
			}
			if err != nil {
				return nil, err
			}
			return func() { l.Unlock() }, nil
		},
		Handover: tui.NewHandover(tool, system.ExecRunner{}),
		Elevate:  plainLogin(env, tool),
	}, nil
}

// plainLogin runs the privilege tool's password prompt straight on the
// terminal, for plain mode, then keeps the login fresh.
func plainLogin(env Env, tool system.Tool) step.ElevateFunc {
	return func(ctx context.Context) error {
		if !env.TTY {
			return errors.New("root steps need a password prompt; run setup in a terminal")
		}
		cmd := tui.LoginCommand(tool)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w: %w", tui.ErrLoginFailed, err)
		}
		system.StartKeepAlive(ctx, system.ExecRunner{}, tool)
		return nil
	}
}

func newSetupCommand(env Env, opts *Options) *cobra.Command {
	var budget, reports, kernel string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Guide this machine through setup, step by step",
		Long: "Checks the machine, installs what the sandbox needs, sets up the host\n" +
			"network and builds the guest image, showing every root command and file\n" +
			"before it runs. Then it runs the selftest. Re-running skips what holds.\n\n" +
			"--yes runs without questions: the API key comes from CODECHECK_API_KEY,\n" +
			"the other answers from flags or config.toml.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, env, opts, setupFlags{budget: budget, reports: reports, kernel: kernel})
		},
	}
	f := cmd.Flags()
	f.StringVar(&budget, "budget", "", "default budget per review in US dollars (saved to config.toml)")
	f.StringVar(&reports, "reports-dir", "", "where reports go (saved to config.toml)")
	f.StringVar(&kernel, "kernel", "", "use this guest kernel file when the pinned download fails (it must match the pin)")
	return cmd
}

type setupFlags struct{ budget, reports, kernel string }

func runSetup(cmd *cobra.Command, env Env, opts *Options, flags setupFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out := cmd.OutOrStdout()
	if env.EUID == 0 {
		return errors.New("run setup as your normal user; it asks for root only for the steps that need it")
	}
	cfg, _, err := env.LoadConfig()
	if err != nil {
		return err
	}
	if flags.budget != "" {
		if cfg.BudgetUSD, err = config.NormaliseBudget(flags.budget); err != nil {
			return fmt.Errorf("--budget: %w", err)
		}
	}
	if flags.reports != "" {
		if _, err := config.ExpandReportsDir(flags.reports, env.Home); err != nil {
			return fmt.Errorf("--reports-dir: %w", err)
		}
		cfg.ReportsDir = flags.reports
	}
	if flags.kernel != "" {
		if !filepath.IsAbs(flags.kernel) {
			return errors.New("--kernel: give the file's full path")
		}
		if fi, err := os.Stat(flags.kernel); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("--kernel %s: not a file", flags.kernel)
		}
	}

	interactive := env.TTY && !opts.Plain && !opts.Yes
	key := env.Environ["CODECHECK_API_KEY"]
	if interactive && !opts.DryRun {
		answers, err := askDetails(env, cfg, opts)
		if err != nil {
			return err
		}
		cfg, key = answers.cfg, answers.key
	}
	if key == "" && !opts.DryRun {
		return errors.New("setup needs the API key: run it in a terminal, or set CODECHECK_API_KEY with --yes")
	}
	if !opts.DryRun {
		if err := env.SaveConfig(cfg); err != nil {
			return err
		}
	}

	host, err := env.OpenSetup(*opts)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var created []step.Item
	d := host.Deps
	d.Record = func(it step.Item) { mu.Lock(); created = append(created, it); mu.Unlock() }
	d.APIKey = func() (string, error) {
		if key == "" {
			return "", errors.New("no API key: set CODECHECK_API_KEY")
		}
		return key, nil
	}
	d.KernelFile, d.CodeCheckCommit = flags.kernel, cfg.CodeCheckCommit
	steps, err := host.Steps(d)
	if err != nil {
		return err
	}

	unlock, err := host.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	runOpts := step.Options{DryRun: opts.DryRun, AssumeYes: opts.Yes,
		OnStepFinished: func(r step.StepResult) {
			recordEvent(ctx, telemetry.StepFinished{Step: telemetry.StepID(r.ID), Outcome: telemetry.Outcome(r.Outcome),
				Duration: telemetry.DurationBandOf(r.Duration)})
		}}
	runErr := runEngine(ctx, env, opts, host, steps, runOpts, out)

	if !opts.DryRun && len(created) > 0 {
		if err := saveManifest(ctx, host, created); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("recording what setup installed: %w", err))
		}
	}
	if runErr != nil || opts.DryRun {
		return runErr
	}
	fmt.Fprintln(out, "Setup finished. Running the selftest…")
	return verifySandbox(cmd, env, opts)
}

// runEngine runs the steps on the full-screen interface, or as plain lines.
func runEngine(ctx context.Context, env Env, opts *Options, host *SetupHost, steps []step.Step, runOpts step.Options, out io.Writer) error {
	run := func(ctx context.Context, o step.Options) error {
		o.DryRun, o.AssumeYes, o.OnStepFinished = runOpts.DryRun, runOpts.AssumeYes, runOpts.OnStepFinished
		return step.Run(ctx, steps, o)
	}
	if env.TTY && !opts.Plain && !opts.Yes {
		app := tui.New(tui.Config{
			Title: "CodeCheck Sandbox setup", Theme: theme.New(lipgloss.HasDarkBackground(os.Stdin, os.Stdout)),
			Sidebar: setup.Sidebar(), Steps: setup.Info(steps), Run: run, Handover: host.Handover,
		})
		if _, err := tea.NewProgram(app, tea.WithContext(ctx)).Run(); err != nil {
			return err
		}
		return app.Err()
	}
	var in io.Reader
	if env.TTY {
		in = env.In
	}
	return tui.RunPlain(ctx, tui.PlainConfig{Out: out, In: in, AssumeYes: opts.Yes, Elevate: host.Elevate,
		Steps: setup.Info(steps), Run: run})
}

// saveManifest adds the items this run created to the installed manifest.
func saveManifest(ctx context.Context, host *SetupHost, created []step.Item) error {
	m, err := host.LoadManifest()
	if err != nil {
		return err
	}
	if m == nil {
		m = &step.Manifest{Version: step.ManifestVersion}
	}
	m.InstalledBy = step.InstalledBy{Version: buildinfo.Version, At: time.Now().UTC().Format(time.RFC3339)}
	for _, it := range created {
		if !slices.Contains(m.Items, it) {
			m.Items = append(m.Items, it)
		}
	}
	return host.SaveManifest(ctx, m)
}

// verifySandbox runs the selftest after a setup.
func verifySandbox(cmd *cobra.Command, env Env, opts *Options) error {
	sb, err := env.OpenSandbox(*opts)
	if err != nil {
		return err
	}
	res, err := runVerify(cmd, env, opts, "selftest", func(ctx context.Context, status func(review.Status), console io.Writer) (review.RunResult, error) {
		return sb.Selftest(ctx, nil, status, console)
	})
	if err != nil {
		return err
	}
	p := res.Summary.Probes
	if p == nil {
		p = &review.Probes{}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Selftest: %d passed, %d failed\n", p.Passed, p.Failed)
	if res.Summary.Outcome != review.OutcomeOK {
		return &ErrOutcome{Outcome: res.Summary.Outcome, ReportDir: res.ReportDir}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "The sandbox works. Try codecheck-sandbox canary (about US$1-5), then review a repository.")
	return nil
}
