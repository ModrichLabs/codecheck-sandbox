package cli

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
)

// fakeSandbox records what the commands asked of the sandbox.
type fakeSandbox struct {
	reviews   []review.Options
	selftests [][]int
	result    review.RunResult
	err       error
}

func (f *fakeSandbox) Review(ctx context.Context, o review.Options, status func(review.Status), console io.Writer) (review.RunResult, error) {
	f.reviews = append(f.reviews, o)
	status(review.Status{State: review.StatePreparing, Started: time.Now()})
	status(review.Status{State: review.StateDone, Started: time.Now()})
	return f.result, f.err
}

func (f *fakeSandbox) Selftest(ctx context.Context, ports []int, status func(review.Status), console io.Writer) (review.RunResult, error) {
	f.selftests = append(f.selftests, ports)
	return f.result, f.err
}

func sandboxEnv(f *fakeSandbox) Env {
	env := testEnv()
	env.Sandbox = func(Env, Options) (Sandbox, error) { return f, nil }
	return env
}

func okResult() review.RunResult {
	return review.RunResult{ReportDir: "/home/u/codecheck-reports/x", Summary: review.Summary{Outcome: review.OutcomeOK}}
}

func TestReviewPassesItsFlags(t *testing.T) {
	f := &fakeSandbox{result: okResult()}
	out, err := runIn(t, sandboxEnv(f), "review", "https://github.com/acme/widget", "--pr", "42",
		"--cwe", "89", "--cwe", "CWE-79", "--prompt", "Review ./routes", "--budget", "$10")
	if err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}
	if len(f.reviews) != 1 {
		t.Fatalf("reviews = %v", f.reviews)
	}
	o := f.reviews[0]
	if o.Source.Target != "https://github.com/acme/widget" || o.Source.PR != 42 || o.BudgetUSD != "10" ||
		!slices.Equal(o.CWEs, []int{89, 79}) || o.Prompt != "Review ./routes" {
		t.Errorf("options = %+v", o)
	}
	if !strings.Contains(out, "/home/u/codecheck-reports/x") {
		t.Errorf("the report folder doesn't show:\n%s", out)
	}
}

func TestReviewTakesTheBudgetFromConfig(t *testing.T) {
	f := &fakeSandbox{result: okResult()}
	env := sandboxEnv(f)
	c, _, _ := env.LoadConfig()
	c.BudgetUSD = "7.50"
	env.SaveConfig(c)
	if _, err := runIn(t, env, "review", "/home/u/src"); err != nil {
		t.Fatal(err)
	}
	if f.reviews[0].BudgetUSD != "7.50" {
		t.Errorf("budget = %q", f.reviews[0].BudgetUSD)
	}
}

func TestReviewRefusesBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no target":       {},
		"two targets":     {"a", "b"},
		"pr and base":     {"x", "--pr", "1", "--base", "main"},
		"pr zero":         {"x", "--pr", "0"},
		"bad budget":      {"x", "--budget", "lots"},
		"budget too big":  {"x", "--budget", "5000"},
		"cwe word":        {"x", "--cwe", "xss"},
		"cwe repeated":    {"x", "--cwe", "89", "--cwe", "89"},
		"cwe too long":    {"x", "--cwe", "123456"},
		"too many cwes":   {"x", "--cwe", "1", "--cwe", "2", "--cwe", "3", "--cwe", "4", "--cwe", "5", "--cwe", "6", "--cwe", "7", "--cwe", "8", "--cwe", "9", "--cwe", "10", "--cwe", "11"},
		"prompt newline":  {"x", "--prompt", "one\ntwo"},
		"prompt escape":   {"x", "--prompt", "a\x1b[2Jb"},
		"prompt too long": {"x", "--prompt", strings.Repeat("x", 1001)},
		"empty prompt":    {"x", "--prompt", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeSandbox{result: okResult()}
			if _, err := runIn(t, sandboxEnv(f), append([]string{"review"}, args...)...); err == nil {
				t.Error("review accepted it")
			}
			if len(f.reviews) != 0 {
				t.Error("the sandbox ran anyway")
			}
		})
	}
}

// Only a clean outcome exits 0; anything else reports first, then fails.
func TestReviewOutcomeDecidesTheExitCode(t *testing.T) {
	for outcome, ok := range map[review.Outcome]bool{
		review.OutcomeOK: true, review.OutcomeBudget: false, review.OutcomeRefused: false,
		review.OutcomeError: false, review.OutcomeIncomplete: false,
	} {
		f := &fakeSandbox{result: review.RunResult{ReportDir: "/r", Summary: review.Summary{Outcome: outcome}}}
		out, err := runIn(t, sandboxEnv(f), "review", "x")
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", outcome, err)
		}
		if !strings.Contains(out, "/r") {
			t.Errorf("%s: no report folder in %q", outcome, out)
		}
	}
	f := &fakeSandbox{err: errors.New("cc-launch exit 3")}
	if _, err := runIn(t, sandboxEnv(f), "review", "x"); err == nil || !strings.Contains(err.Error(), "cc-launch exit 3") {
		t.Errorf("a failed run: %v", err)
	}
}

func TestReviewPlainShowsProgress(t *testing.T) {
	f := &fakeSandbox{result: okResult()}
	out, _ := runIn(t, sandboxEnv(f), "review", "x")
	if !strings.Contains(out, "preparing") || !strings.Contains(out, "done") {
		t.Errorf("no status lines:\n%s", out)
	}
}
