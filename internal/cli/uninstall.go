package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guest"
	"github.com/ModrichLabs/codecheck-sandbox/internal/hostnet"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/setup"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

func newUninstallCommand(env Env, opts *Options) *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove everything setup installed",
		Long: "Removes what setup recorded creating, in reverse order. Packages and\n" +
			"users that existed before setup stay. Without the manifest it scans for\n" +
			"the sandbox's own files, units, user and network items instead, and leaves\n" +
			"every package. Reports stay unless --purge, which also removes the work\n" +
			"folder and the build cache.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, env, opts, purge)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the reports folder, the work folder and the build cache")
	return cmd
}

func runUninstall(cmd *cobra.Command, env Env, opts *Options, purge bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out := cmd.OutOrStdout()
	if env.EUID == 0 {
		return errors.New("run uninstall as your normal user; it asks for root where it needs it")
	}
	cfg, _, err := env.LoadConfig()
	if err != nil {
		return err
	}
	reports, _ := cfg.ReportsPath(env.Home)
	host, err := env.OpenSetup(*opts)
	if err != nil {
		return err
	}
	d := host.Deps
	d.CodeCheckCommit = cfg.CodeCheckCommit
	steps, err := host.Steps(d)
	if err != nil {
		return err
	}
	m, err := host.LoadManifest()
	if err != nil {
		return err
	}
	if m == nil {
		fmt.Fprintln(out, "No manifest records what setup installed, so uninstall falls back to a footprint scan:")
		fmt.Fprintln(out, "it removes the sandbox's own files, units, user and network items it finds, and no package.")
		m = &step.Manifest{Version: step.ManifestVersion, Items: footprintScan(host)}
	}

	if !confirmUninstall(env, opts, out, m.Items, reports, purge) {
		return errors.New("uninstall: nothing removed")
	}
	if opts.DryRun {
		return nil
	}
	unlock, err := host.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	run := func(ctx context.Context, o step.Options) error { return step.Uninstall(ctx, steps, m, o) }
	if err := runUndo(ctx, env, opts, host, steps, run, out); err != nil {
		return fmt.Errorf("uninstall left some items in place (the manifest keeps them, so run it again): %w", err)
	}
	if host.RemoveManifest != nil {
		if err := host.RemoveManifest(ctx); err != nil {
			return err
		}
	}
	if purge {
		cache := filepath.Join(env.Home, ".cache", "codecheck-sandbox")
		if b := env.Environ["XDG_CACHE_HOME"]; filepath.IsAbs(b) {
			cache = filepath.Join(b, "codecheck-sandbox")
		}
		for _, dir := range []string{reports, filepath.Join(env.Home, "codecheck-work"), cache} {
			if dir == "" {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(out, "Uninstalled.")
	return nil
}

// confirmUninstall shows what goes and what stays, then asks (or takes
// --yes). A dry run only shows.
func confirmUninstall(env Env, opts *Options, out io.Writer, items []step.Item, reports string, purge bool) bool {
	if env.TTY && !opts.Plain && !opts.Yes && !opts.DryRun {
		u := tui.NewUninstall(theme.New(lipgloss.HasDarkBackground(os.Stdin, os.Stdout)), items, reports, purge)
		if _, err := tea.NewProgram(u).Run(); err != nil {
			return false
		}
		return u.Confirmed()
	}
	fmt.Fprintln(out, "These go:")
	for _, it := range items {
		if it.State == step.Created {
			fmt.Fprintf(out, "  %s %s%s\n", it.Kind, it.Path, it.Name)
		}
	}
	if purge {
		fmt.Fprintf(out, "  reports %s\n", reports)
	}
	fmt.Fprintln(out, "These stay:")
	for _, it := range items {
		if it.State != step.Created {
			fmt.Fprintf(out, "  %s %s%s (here before setup)\n", it.Kind, it.Path, it.Name)
		}
	}
	if !purge {
		fmt.Fprintf(out, "  %s (your reports; --purge removes them)\n", reports)
	}
	return opts.Yes || opts.DryRun
}

// runUndo runs the undo on the interface's screens or as plain lines.
func runUndo(ctx context.Context, env Env, opts *Options, host *SetupHost, steps []step.Step, run tui.RunFunc, out io.Writer) error {
	if env.TTY && !opts.Plain {
		app := tui.New(tui.Config{
			Title: "CodeCheck Sandbox uninstall", Theme: theme.New(lipgloss.HasDarkBackground(os.Stdin, os.Stdout)),
			Sidebar: setup.Sidebar(), Steps: setup.Info(steps), Run: run, Handover: host.Handover,
		})
		if _, err := tea.NewProgram(app, tea.WithContext(ctx)).Run(); err != nil {
			return err
		}
		return app.Err()
	}
	return tui.RunPlain(ctx, tui.PlainConfig{Out: out, AssumeYes: true, Elevate: host.Elevate, Steps: setup.Info(steps), Run: run})
}

// footprintScan lists the footprint items present on the host. File and
// folder items need their path; the jail user needs to exist; other named
// items (units, the tap, the nft table) count when their step left a file.
func footprintScan(host *SetupHost) []step.Item {
	all := append(hostnet.Footprint(hostnet.DefaultConfig()), guest.Footprint(launch.DefaultPaths())...)
	exists := host.Exists
	if exists == nil {
		exists = func(string) bool { return false }
	}
	withFile := map[string]bool{}
	for _, it := range all {
		if (it.Kind == step.KindFile || it.Kind == step.KindDir) && exists(it.Path) {
			withFile[it.ID] = true
		}
	}
	var out []step.Item
	for _, it := range all {
		switch {
		case it.Kind == step.KindFile || it.Kind == step.KindDir:
			if exists(it.Path) {
				out = append(out, it)
			}
		case it.Kind == step.KindUser:
			if host.UserExists != nil && host.UserExists(it.Name) {
				out = append(out, it)
			}
		case withFile[it.ID]:
			out = append(out, it)
		}
	}
	return out
}
