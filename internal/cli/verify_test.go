package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestSelftestPassRule(t *testing.T) {
	pass := &fakeSandbox{result: review.RunResult{ReportDir: "/r", Summary: review.Summary{
		Outcome: review.OutcomeOK, Probes: &review.Probes{Passed: 9}}}}
	out, err := runIn(t, sandboxEnv(pass), "selftest", "--port", "8080", "--port", "5432")
	if err != nil {
		t.Fatalf("a clean selftest failed: %v\n%s", err, out)
	}
	if !slices.Equal(pass.selftests[0], []int{5432, 8080}) {
		t.Errorf("ports = %v", pass.selftests[0])
	}
	if !strings.Contains(out, "9 passed") {
		t.Errorf("no tally:\n%s", out)
	}

	fail := &fakeSandbox{result: review.RunResult{ReportDir: "/r", Summary: review.Summary{
		Outcome: review.OutcomeFailed, Probes: &review.Probes{Passed: 8, Failed: 1, FailedIDs: []string{"docker-port"}}}}}
	out, err = runIn(t, sandboxEnv(fail), "selftest")
	if err == nil || !strings.Contains(out+err.Error(), "docker-port") {
		t.Errorf("a failed probe: %v\n%s", err, out)
	}
	unfinished := &fakeSandbox{result: review.RunResult{Summary: review.Summary{Outcome: review.OutcomeIncomplete, Probes: &review.Probes{}}}}
	if _, err := runIn(t, sandboxEnv(unfinished), "selftest"); err == nil {
		t.Error("an unfinished selftest passed")
	}
}

func TestSelftestRefusesBadPorts(t *testing.T) {
	for _, p := range []string{"0", "70000", "x"} {
		f := &fakeSandbox{}
		if _, err := runIn(t, sandboxEnv(f), "selftest", "--port", p); err == nil || len(f.selftests) != 0 {
			t.Errorf("--port %s: %v", p, err)
		}
	}
}

func TestListeningPorts(t *testing.T) {
	ss := "LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\n" +
		"LISTEN 0 4096 127.0.0.1:631 0.0.0.0:*\n" +
		"LISTEN 0 4096 172.16.0.1:3180 0.0.0.0:*\n" +
		"LISTEN 0 4096 [::]:8080 [::]:*\n" +
		"LISTEN 0 4096 *:5432 *:*\n" +
		"LISTEN 0 4096 172.16.0.1:9000 0.0.0.0:*\n" +
		"LISTEN 0 4096 [::1]:6379 [::]:*\n" +
		"LISTEN 0 4096 192.168.1.5:443 0.0.0.0:*\n"
	r := &system.FakeRunner{}
	r.On("ss", "-Hltn").Return(system.Result{Stdout: []byte(ss)})
	got := listeningPorts(context.Background(), r)
	// Loopback, the proxy's own port and other interfaces' addresses the
	// VM can't reach drop out; everything bound to any address or the tap
	// stays.
	if !slices.Equal(got, []int{22, 5432, 8080, 9000}) {
		t.Errorf("ports = %v", got)
	}
}

// canaryResult fakes a canary review's report.
func canaryResult(t *testing.T, report string, s review.Summary) review.RunResult {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.md"), []byte(report), 0o600)
	return review.RunResult{ReportDir: dir, Summary: s}
}

func TestCanaryPassRule(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	found := "# Findings\n\n## SQL injection in users\n\n- CWE: CWE-89\n"
	for name, c := range map[string]struct {
		report string
		sum    review.Summary
		pass   bool
	}{
		"found it":       {found, review.Summary{Outcome: review.OutcomeOK}, true},
		"missed it":      {"# Findings\n\nNone.\n", review.Summary{Outcome: review.OutcomeOK}, false},
		"proxy refused":  {found, review.Summary{Outcome: review.OutcomeOK, ProxyDenials: 1, DeniedHosts: []string{"statsig.anthropic.com"}}, false},
		"budget stopped": {found, review.Summary{Outcome: review.OutcomeBudget}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeSandbox{result: canaryResult(t, c.report, c.sum)}
			env := sandboxEnv(f)
			env.Home = t.TempDir()
			out, err := runIn(t, env, "canary")
			if (err == nil) != c.pass {
				t.Errorf("err = %v\n%s", err, out)
			}
			if len(f.reviews) != 1 {
				t.Fatalf("reviews = %v", f.reviews)
			}
			o := f.reviews[0]
			if o.BudgetUSD != "5" || !slices.Equal(o.CWEs, []int{89}) {
				t.Errorf("canary options = %+v", o)
			}
			// The sample got materialised as a git repository for the run,
			// and cleared away afterwards.
			if !f.targetGit[0] {
				t.Errorf("the canary target %s held no git repository during the run", o.Source.Target)
			}
			if _, err := os.Stat(o.Source.Target); err == nil {
				t.Error("the canary sample outlived the run")
			}
		})
	}
}
