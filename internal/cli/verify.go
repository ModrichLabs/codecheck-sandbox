package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// maxProbePorts caps the extra host ports the selftest tries; the guest's
// settings block holds 128 bytes for them.
const maxProbePorts = 15

// tapHost names the host's address on the review tap.
var tapHost = netip.MustParseAddr("172.16.0.1")

// proxyPort serves the VM on purpose, so the selftest skips it.
const proxyPort = 3180

func newSelftestCommand(env Env, opts *Options) *cobra.Command {
	var ports []int
	cmd := &cobra.Command{
		Use:   "selftest",
		Short: "Boot a VM and try each way out of the sandbox",
		Long: "Boots a VM that tries every way out it can think of: DNS, other host\n" +
			"ports, IPv6 link-local, a spoofed source address, and the host's own\n" +
			"listening ports. It passes only when every probe gets stopped.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, p := range ports {
				if p < 1 || p > 65535 {
					return fmt.Errorf("--port %d: want 1 to 65535", p)
				}
			}
			all, err := selftestPorts(cmd.Context(), env, ports, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			sb, err := env.OpenSandbox(*opts)
			if err != nil {
				return err
			}
			stop, err := sb.Login(cmd.Context())
			if err != nil {
				return err
			}
			defer stop()
			res, err := runVerify(cmd, env, opts, "selftest", func(ctx context.Context, status func(review.Status), console io.Writer) (review.RunResult, error) {
				return sb.Selftest(ctx, all, status, console)
			})
			if err != nil {
				return err
			}
			p := res.Summary.Probes
			if p == nil {
				p = &review.Probes{}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Selftest: %d passed, %d failed\n", p.Passed, p.Failed)
			switch {
			case p.Failed > 0:
				return fmt.Errorf("selftest: %d probe(s) got out of the sandbox: %s; see %s", p.Failed, strings.Join(p.FailedIDs, ", "), res.ReportDir)
			case res.Summary.Outcome != review.OutcomeOK:
				return &ErrOutcome{Outcome: res.Summary.Outcome, ReportDir: res.ReportDir}
			}
			return nil
		},
	}
	cmd.Flags().IntSliceVar(&ports, "port", nil, "also try this host port (repeatable)")
	return cmd
}

// selftestPorts gathers the ports a selftest probes: the user's first, then
// the host's listening ports up to the cap, with a warning for any left out.
func selftestPorts(ctx context.Context, env Env, user []int, warn io.Writer) ([]int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var listening []int
	if env.ListeningPorts != nil {
		listening = env.ListeningPorts(ctx)
	}
	return probePorts(user, listening, warn)
}

// probePorts keeps every user port (refusing more than the cap) and fills
// the rest with listening ports, naming those that didn't fit.
func probePorts(user, listening []int, warn io.Writer) ([]int, error) {
	out := slices.Clone(user)
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > maxProbePorts {
		return nil, fmt.Errorf("--port: at most %d ports", maxProbePorts)
	}
	var dropped []string
	for _, p := range listening {
		switch {
		case slices.Contains(out, p):
		case len(out) < maxProbePorts:
			out = append(out, p)
		default:
			dropped = append(dropped, strconv.Itoa(p))
		}
	}
	if len(dropped) > 0 {
		fmt.Fprintf(warn, "selftest: %d listening port(s) stay untested (the probe takes %d): %s\n",
			len(dropped), maxProbePorts, strings.Join(dropped, ", "))
	}
	slices.Sort(out)
	return out, nil
}

// listeningPorts lists the TCP ports the host listens on where the VM could
// reach them: on every address, or on the tap's. Loopback and other
// interfaces' addresses stay out, and so does the proxy's own port.
func listeningPorts(ctx context.Context, r system.Runner) []int {
	res, err := r.Run(ctx, system.Cmd{Name: "ss", Args: []string{"-Hltn"}})
	if err != nil || res.Code != 0 {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		local := f[3]
		i := strings.LastIndexByte(local, ':')
		if i < 0 {
			continue
		}
		host, port := strings.Trim(local[:i], "[]"), local[i+1:]
		host, _, _ = strings.Cut(host, "%") // *%tap0, fe80::1%eth0
		n, err := strconv.Atoi(port)
		if err != nil || n == proxyPort {
			continue
		}
		if host == "*" {
			ports = append(ports, n)
			continue
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			continue
		}
		if addr = addr.Unmap(); addr.IsUnspecified() || addr == tapHost {
			ports = append(ports, n)
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}

// canaryCWE names the weakness the canary sample plants.
const canaryCWE = 89

func newCanaryCommand(env Env, opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "canary",
		Short: "Run a small paid review of a known-vulnerable sample",
		Long: "Reviews a tiny sample with a planted SQL injection (CWE-89), with a US$5\n" +
			"budget (it usually costs US$1-5). It passes when the review ends cleanly,\n" +
			"reports the injection, and the proxy refused nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src := filepath.Join(env.Home, "codecheck-work", "canary-src")
			if err := materialiseCanary(cmd.Context(), src); err != nil {
				return fmt.Errorf("canary: preparing the sample: %w", err)
			}
			defer os.RemoveAll(src)
			sb, err := env.OpenSandbox(*opts)
			if err != nil {
				return err
			}
			stop, err := sb.Login(cmd.Context())
			if err != nil {
				return err
			}
			defer stop()
			o := review.Options{Source: review.Source{Target: src}, BudgetUSD: "5", CWEs: []int{canaryCWE}}
			res, err := runVerify(cmd, env, opts, "canary", func(ctx context.Context, status func(review.Status), console io.Writer) (review.RunResult, error) {
				return sb.Review(ctx, o, status, console)
			})
			if err != nil {
				return err
			}
			printSummary(cmd.OutOrStdout(), res)
			return canaryVerdict(res)
		},
	}
}

// canaryVerdict applies the pass rule.
func canaryVerdict(res review.RunResult) error {
	s := res.Summary
	if s.Outcome != review.OutcomeOK {
		return &ErrOutcome{Outcome: s.Outcome, ReportDir: res.ReportDir}
	}
	if s.ProxyDenials > 0 {
		return fmt.Errorf("canary: the proxy refused %d request(s) (%s); Claude Code needs a host the allowlist lacks",
			s.ProxyDenials, strings.Join(s.DeniedHosts, ", "))
	}
	b, err := os.ReadFile(filepath.Join(res.ReportDir, "report.md"))
	if err != nil {
		return fmt.Errorf("canary: %w", err)
	}
	if !regexp.MustCompile(fmt.Sprintf(`\bCWE-%d\b`, canaryCWE)).Match(b) {
		return fmt.Errorf("canary: the report misses the planted CWE-%d; see %s", canaryCWE, res.ReportDir)
	}
	return nil
}

// materialiseCanary writes the embedded sample to dir as a one-commit git
// repository, which the review pipeline clones like any other.
func materialiseCanary(ctx context.Context, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	err := fs.WalkDir(assets.Canary, "canary", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := assets.Canary.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, strings.TrimPrefix(path, "canary/")), b, 0o600)
	})
	if err != nil {
		return err
	}
	git := func(args ...string) error {
		res, err := system.ExecRunner{}.Run(ctx, system.Cmd{
			Name: "git", Args: append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
				"-c", "user.name=codecheck-sandbox", "-c", "user.email=canary@codecheck-sandbox.invalid"}, args...),
			Dir: dir, CleanEnv: true, Env: []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"},
		})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(res.Stderr)))
		}
		return nil
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "canary sample"}} {
		if err := git(args...); err != nil {
			return err
		}
	}
	return nil
}

// runVerify runs a selftest or canary on the review screen, or with status
// lines in plain mode.
func runVerify(cmd *cobra.Command, env Env, opts *Options, title string,
	run func(ctx context.Context, status func(review.Status), console io.Writer) (review.RunResult, error)) (review.RunResult, error) {
	if env.TTY && !opts.Plain {
		th := theme.New(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
		m := tui.NewReview(th, title, func(ctx context.Context, status func(review.Status)) (review.RunResult, error) {
			return run(ctx, status, io.Discard)
		})
		if _, err := tea.NewProgram(m, tea.WithContext(cmd.Context())).Run(); err != nil {
			return review.RunResult{}, err
		}
		return m.Result(), m.Err()
	}
	out := cmd.OutOrStdout()
	status := func(s review.Status) {
		fmt.Fprintf(out, "%s: %s (%s)\n", title, s.State, time.Since(s.Started).Round(time.Second))
	}
	res, err := run(cmd.Context(), status, io.Discard)
	if err != nil && errors.Is(err, context.Canceled) {
		return res, fmt.Errorf("%s: stopped", title)
	}
	return res, err
}
