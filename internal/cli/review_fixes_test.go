package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// A reports folder of ~ must never let --purge take the home folder.
func TestPurgeRemovesOnlyReportFolders(t *testing.T) {
	h := &uninstallHost{undone: map[string][]step.Item{}, manifest: &step.Manifest{Version: step.ManifestVersion}}
	env := h.env(t)
	reports := filepath.Join(env.Home, "stuff")
	os.MkdirAll(filepath.Join(reports, "widget-20261122-100000"), 0o700)
	os.WriteFile(filepath.Join(reports, "widget-20261122-100000", "summary.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(reports, "thesis.tex"), []byte("mine"), 0o600)
	os.MkdirAll(filepath.Join(reports, "photos"), 0o700)
	c, _, _ := env.LoadConfig()
	c.ReportsDir = reports
	env.SaveConfig(c)
	if _, err := runIn(t, env, "uninstall", "--yes", "--purge"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(reports, "widget-20261122-100000")); err == nil {
		t.Error("the report folder survived --purge")
	}
	for _, keep := range []string{"thesis.tex", "photos"} {
		if _, err := os.Stat(filepath.Join(reports, keep)); err != nil {
			t.Errorf("--purge removed %s, which no review made", keep)
		}
	}
}

func TestReportsFolderCantBeHomeOrAbove(t *testing.T) {
	for _, s := range []string{"~", "/home", "/home/u", "/"} {
		if _, err := config.ExpandReportsDir(s, "/home/u"); err == nil {
			t.Errorf("ExpandReportsDir(%q) passed", s)
		}
	}
	if _, err := config.ExpandReportsDir("~/codecheck-reports", "/home/u"); err != nil {
		t.Error(err)
	}
}

// doas and run0 ask for a password per run; that prompt must come before
// the full-screen interface takes the terminal.
func TestLoginComesBeforeTheRun(t *testing.T) {
	f := &fakeSandbox{result: okResult()}
	if _, err := runIn(t, sandboxEnv(f), "review", "x"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.order, []string{"login", "review", "stop"}) {
		t.Errorf("order = %v", f.order)
	}
}

func TestProbePortsPutTheUsersFirst(t *testing.T) {
	var listening []int
	for p := 1; p <= 30; p++ {
		listening = append(listening, p)
	}
	var warn bytes.Buffer
	got, err := probePorts([]int{8080, 5432}, listening, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxProbePorts || !slices.Contains(got, 8080) || !slices.Contains(got, 5432) {
		t.Errorf("ports = %v", got)
	}
	if !strings.Contains(warn.String(), "untested") {
		t.Errorf("no warning about dropped ports: %q", warn.String())
	}
	many := make([]int, maxProbePorts+1)
	for i := range many {
		many[i] = 9000 + i
	}
	if _, err := probePorts(many, nil, &warn); err == nil {
		t.Error("more --port values than the cap passed")
	}
}

func TestSetupSelftestProbesTheListeningPorts(t *testing.T) {
	f := &fakeSetup{steps: []scriptStep{{id: "components", phase: step.PhaseComponents, ok: true}}}
	sb := passingSelftest()
	env := setupEnv(f, sb)
	env.ListeningPorts = func(context.Context) []int { return []int{22, 5432} }
	if _, err := runIn(t, env, "setup", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(sb.selftests) != 1 || !slices.Equal(sb.selftests[0], []int{22, 5432}) {
		t.Errorf("setup's selftest probed %v", sb.selftests)
	}
}

func TestCanaryNeedsTheExactCWE(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.md"), []byte("- CWE: CWE-893\n"), 0o600)
	err := canaryVerdict(review.RunResult{ReportDir: dir, Summary: review.Summary{Outcome: review.OutcomeOK}})
	if err == nil {
		t.Error("CWE-893 passed for CWE-89")
	}
}

func TestBundleListShowsRedactedSizes(t *testing.T) {
	env := bundleEnv(t)
	dest := filepath.Join(t.TempDir(), "b.tar.gz")
	out, err := runIn(t, env, "doctor", "--bundle", "--out", dest)
	if err != nil {
		t.Fatal(err)
	}
	files := readBundle(t, dest)
	for name, body := range files {
		if !strings.Contains(out, name+" ("+strconv.Itoa(len(body))+" bytes)") {
			t.Errorf("the list's size for %s differs from the archive's %d:\n%s", name, len(body), out)
		}
	}
	if _, err := runIn(t, env, "doctor", "--out", dest); err == nil {
		t.Error("--out without --bundle passed")
	}
}

func TestTelemetryOnSaysWhatStillBlocksIt(t *testing.T) {
	env := testEnv()
	env.Environ["DO_NOT_TRACK"] = "1"
	out, err := runIn(t, env, "telemetry", "on")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "DO_NOT_TRACK") || strings.Contains(out, "go to PostHog") {
		t.Errorf("output: %q", out)
	}
}

func TestListeningPortsReadEveryReachableForm(t *testing.T) {
	r := &system.FakeRunner{}
	r.On("ss", "-Hltn").Return(system.Result{Stdout: []byte(
		"LISTEN 0 4096 *%tap0:8000 *:*\n" +
			"LISTEN 0 4096 [::ffff:172.16.0.1]:8001 [::]:*\n" +
			"LISTEN 0 4096 [::ffff:127.0.0.1]:8002 [::]:*\n")})
	if got := listeningPorts(context.Background(), r); !slices.Equal(got, []int{8000, 8001}) {
		t.Errorf("ports = %v", got)
	}
}
