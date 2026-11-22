package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// ErrOutcome reports a review that ran but didn't end cleanly; the report
// still got written.
type ErrOutcome struct {
	Outcome   review.Outcome
	ReportDir string
}

func (e *ErrOutcome) Error() string {
	return fmt.Sprintf("the review ended %s; see %s", e.Outcome, e.ReportDir)
}

func newReviewCommand(env Env, opts *Options) *cobra.Command {
	var (
		pr     int
		base   string
		prompt string
		cwes   []string
		budget string
	)
	cmd := &cobra.Command{
		Use:   "review <path|url>",
		Short: "Review a repository inside a sandboxed VM",
		Long: "Clones a fresh copy on the host, strips git hooks and configuration, and\n" +
			"reviews it with CodeCheck inside a jailed VM. It exits 0 only when the\n" +
			"review ends cleanly; the report gets written either way.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := reviewOptions(env, reviewArgs{target: args[0], pr: pr, prSet: cmd.Flags().Changed("pr"), base: base,
				prompt: prompt, promptSet: cmd.Flags().Changed("prompt"), cwes: cwes, budget: budget})
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
			var res review.RunResult
			if env.TTY && !opts.Plain {
				res, err = reviewScreen(cmd.Context(), sb, o)
			} else {
				res, err = reviewPlain(cmd.Context(), cmd.OutOrStdout(), sb, o)
			}
			if err != nil {
				return err
			}
			printSummary(cmd.OutOrStdout(), res)
			if res.Summary.Outcome != review.OutcomeOK {
				return &ErrOutcome{Outcome: res.Summary.Outcome, ReportDir: res.ReportDir}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&pr, "pr", 0, "review a pull request: its head, diffed against its merge base")
	f.StringVar(&base, "base", "", "review only what changed since this ref")
	f.StringVar(&prompt, "prompt", "", "one line for the reviewer, such as a sub-folder or a threat model")
	f.StringArrayVar(&cwes, "cwe", nil, "hunt this CWE first, such as 89 (repeatable; a priority, not a limit)")
	f.StringVar(&budget, "budget", "", "stop the review at this many US dollars (default from config, US$25)")
	return cmd
}

// reviewArgs holds review's arguments as typed.
type reviewArgs struct {
	target, base, prompt, budget string
	pr                           int
	prSet, promptSet             bool
	cwes                         []string
}

// reviewOptions checks every argument before anything runs.
func reviewOptions(env Env, a reviewArgs) (review.Options, error) {
	var o review.Options
	switch {
	case a.prSet && a.pr < 1:
		return o, errors.New("--pr: want a pull request number")
	case a.prSet && a.base != "":
		return o, errors.New("--pr and --base each pick what to review; pass one")
	}
	budget := a.budget
	if budget == "" {
		c, _, err := env.LoadConfig()
		if err != nil {
			return o, err
		}
		budget = c.BudgetUSD
	}
	b, err := config.NormaliseBudget(budget)
	if err != nil {
		return o, fmt.Errorf("--budget: %w", err)
	}
	for _, s := range a.cwes {
		n, err := parseCWE(s)
		if err != nil {
			return o, err
		}
		o.CWEs = append(o.CWEs, n)
	}
	if err := guestinit.ValidateCWEs(o.CWEs); err != nil {
		return o, fmt.Errorf("--cwe: %w", err)
	}
	if a.promptSet {
		if err := guestinit.ValidatePrompt(a.prompt); err != nil {
			return o, fmt.Errorf("--prompt: %w", err)
		}
		o.Prompt = a.prompt
	}
	o.Source = review.Source{Target: a.target, PR: a.pr, Base: a.base}
	o.BudgetUSD = b
	return o, nil
}

// parseCWE reads 89 or CWE-89.
func parseCWE(s string) (int, error) {
	d := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "CWE-")
	if d == "" || len(d) > 5 || strings.Trim(d, "0123456789") != "" {
		return 0, fmt.Errorf("--cwe %q: want a CWE number such as 89", s)
	}
	n, _ := strconv.Atoi(d)
	return n, nil
}

func reviewPlain(ctx context.Context, out io.Writer, sb Sandbox, o review.Options) (review.RunResult, error) {
	status := func(s review.Status) {
		fmt.Fprintf(out, "review: %s (%s)\n", s.State, time.Since(s.Started).Round(time.Second))
	}
	return sb.Review(ctx, o, status, io.Discard)
}

func reviewScreen(ctx context.Context, sb Sandbox, o review.Options) (review.RunResult, error) {
	th := theme.New(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
	m := tui.NewReview(th, review.DisplayTarget(o.Source.Target), func(ctx context.Context, status func(review.Status)) (review.RunResult, error) {
		return sb.Review(ctx, o, status, io.Discard)
	})
	if _, err := tea.NewProgram(m, tea.WithContext(ctx)).Run(); err != nil {
		return review.RunResult{}, err
	}
	return m.Result(), m.Err()
}

func printSummary(out io.Writer, res review.RunResult) {
	s := res.Summary
	fmt.Fprintf(out, "Outcome: %s · US$%.2f · %d sub-agents\n", s.Outcome, s.Result.CostUSD, s.SubAgents)
	for _, w := range s.Warnings {
		fmt.Fprintln(out, "! "+w)
	}
	if res.ReportDir != "" {
		fmt.Fprintln(out, "Report: "+res.ReportDir)
	}
}
