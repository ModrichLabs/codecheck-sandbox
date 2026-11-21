package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeLaunch plays cc-launch: it prints a console and leaves result files,
// as the real one does, owned by the caller.
type fakeLaunch struct {
	paths   launch.Paths
	console io.Writer
	results map[string]string
	err     error
	calls   []string
}

func (f *fakeLaunch) Run(_ context.Context, id launch.RunID) error {
	f.calls = append(f.calls, "run "+id.String())
	fmt.Fprintf(f.console, "[    0.000000] Linux version 6.1.155\n")
	block, _ := os.ReadFile(filepath.Join(id.Dir(f.paths), "config.img"))
	cfg, _ := guestinit.Parse(block)
	fmt.Fprintf(f.console, "cc-guestinit: %s %s starting\n", cfg.Mode, id)
	if f.results != nil {
		res := filepath.Join(id.Dir(f.paths), "result")
		os.MkdirAll(res, 0o700)
		for name, body := range f.results {
			os.WriteFile(filepath.Join(res, name), []byte(body), 0o600)
		}
	}
	fmt.Fprintf(f.console, "cc-guestinit: %s finished: exit 0\n", cfg.Mode)
	return f.err
}

func (f *fakeLaunch) Clean(_ context.Context, id launch.RunID) error {
	f.calls = append(f.calls, "clean "+id.String())
	return os.RemoveAll(id.Dir(f.paths))
}

// stageRunner plays the privilege tool for StageRun: it makes the folders
// and copies the files for real.
type stageRunner struct{ calls int }

func (s *stageRunner) Run(_ context.Context, c system.Cmd) (system.Result, error) {
	s.calls++
	a := c.Args
	if a[0] == "-d" {
		return system.Result{}, os.MkdirAll(a[len(a)-1], 0o755)
	}
	b, err := os.ReadFile(a[len(a)-2])
	if err != nil {
		return system.Result{}, err
	}
	return system.Result{}, os.WriteFile(a[len(a)-1], b, 0o644)
}

type pipelineRig struct {
	p        *Pipeline
	launcher *fakeLaunch
	states   []State
}

func newPipeline(t *testing.T) *pipelineRig {
	t.Helper()
	needTools(t)
	root := t.TempDir()
	paths := launch.DefaultPaths()
	paths.RunsDir = filepath.Join(root, "runs")
	r := &pipelineRig{launcher: &fakeLaunch{paths: paths}}
	r.p = &Pipeline{
		Run: system.ExecRunner{}, Root: &stageRunner{}, Paths: paths,
		WorkRoot: filepath.Join(root, "work"), ReportsRoot: filepath.Join(root, "reports"),
		Now: func() time.Time { return now },
		Launcher: func(console io.Writer) Launcher {
			r.launcher.console = console
			return r.launcher
		},
		Status: func(s Status) { r.states = append(r.states, s.State) },
	}
	return r
}

func TestReview(t *testing.T) {
	r := newPipeline(t)
	r.launcher.results = map[string]string{
		"stream.jsonl":        resultLine("success", false, 2.5, "# Findings\n\nNone.") + "\n",
		"stderr.log":          "",
		"exit-code":           "0\n",
		"proxy-denials.jsonl": "",
	}
	src := repo(t, "")
	res, err := r.p.Review(context.Background(), Options{Source: Source{Target: src}, BudgetUSD: "25"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Summary.Outcome != OutcomeOK || res.Summary.Result.CostUSD != 2.5 || res.Summary.Target != src || res.Summary.Scope != "full" {
		t.Errorf("summary = %+v", res.Summary)
	}
	if !strings.HasPrefix(res.ReportDir, r.p.ReportsRoot+"/") {
		t.Errorf("report folder %s", res.ReportDir)
	}
	for _, name := range []string{"report.md", "summary.json", "stream.jsonl", "stderr.log", "proxy-denials.jsonl"} {
		if _, err := os.Stat(filepath.Join(res.ReportDir, name)); err != nil {
			t.Errorf("report folder lacks %s", name)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(res.ReportDir, "report.md")); string(b) != "# Findings\n\nNone.\n" {
		t.Errorf("report.md = %q", b)
	}
	if len(r.launcher.calls) != 2 || !strings.HasPrefix(r.launcher.calls[1], "clean ") {
		t.Errorf("launcher calls = %v", r.launcher.calls)
	}
	want := []State{StatePreparing, StateBooting, StateRunning, StateFinished, StateCollecting, StateDone}
	if !slices.Equal(r.states, want) {
		t.Errorf("states = %v, want %v", r.states, want)
	}
	if entries, _ := os.ReadDir(r.p.WorkRoot); len(entries) != 0 {
		t.Errorf("the work folder stayed: %v", entries)
	}
}

func TestReviewKeepsPartialResultsAfterAFailedRun(t *testing.T) {
	r := newPipeline(t)
	// A review that timed out: cc-launch still copied what it could.
	r.launcher.results = map[string]string{"stream.jsonl": initLine + "\n"}
	r.launcher.err = &launch.ExitError{Code: launch.ExitTimeout}
	res, err := r.p.Review(context.Background(), Options{Source: Source{Target: repo(t, "")}, BudgetUSD: "5"})
	var ee *launch.ExitError
	if !errors.As(err, &ee) || ee.Code != launch.ExitTimeout {
		t.Fatalf("Review = %v, want the timeout", err)
	}
	if res.Summary.Outcome != OutcomeIncomplete || res.ReportDir == "" {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.ReportDir, "stream.jsonl")); err != nil {
		t.Error("the partial transcript got lost")
	}
	if !strings.HasPrefix(r.launcher.calls[len(r.launcher.calls)-1], "clean ") {
		t.Errorf("the run never got cleaned: %v", r.launcher.calls)
	}
}

func TestReviewCleansUpWhenPreparationFails(t *testing.T) {
	r := newPipeline(t)
	_, err := r.p.Review(context.Background(), Options{Source: Source{Target: "http://example.com/x"}, BudgetUSD: "5"})
	if err == nil {
		t.Fatal("Review accepted an http source")
	}
	if len(r.launcher.calls) != 0 {
		t.Errorf("cc-launch ran: %v", r.launcher.calls)
	}
}

func TestSelftest(t *testing.T) {
	r := newPipeline(t)
	r.launcher.results = map[string]string{
		"stream.jsonl": "PASS no-dns x\nSUMMARY 1 passed, 0 failed\n",
		"exit-code":    "0\n",
	}
	res, err := r.p.Selftest(context.Background(), []int{8080})
	if err != nil {
		t.Fatalf("Selftest: %v", err)
	}
	if res.Summary.Outcome != OutcomeOK || res.Summary.Probes.Passed != 1 || res.Summary.Mode != "selftest" {
		t.Errorf("summary = %+v", res.Summary)
	}
}

// Two runs in the same second keep separate reports.
func TestSelftestsInTheSameSecondKeepSeparateReports(t *testing.T) {
	r := newPipeline(t)
	r.launcher.results = map[string]string{"stream.jsonl": "SUMMARY 0 passed, 0 failed\n", "exit-code": "0\n"}
	a, err := r.p.Selftest(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.p.Selftest(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ReportDir == b.ReportDir {
		t.Errorf("both runs wrote to %s", a.ReportDir)
	}
}

// The summary names the target without any token from its URL.
func TestSummaryTargetDropsTheToken(t *testing.T) {
	s := summaryFor(Source{Target: "https://user:ghp_secret@example.com/org/repo"}, Prepared{})
	if strings.Contains(s.Target, "ghp_secret") || s.Target != "https://example.com/org/repo" {
		t.Errorf("summary target = %q", s.Target)
	}
}

func TestStatusParser(t *testing.T) {
	var got []State
	w := newStatusWriter("cc-20261121-101500-a1b2c3", func(s State) { got = append(got, s) })
	// Lines may arrive split across writes.
	io.WriteString(w, "[    0.1] booting\ncc-guestinit: review cc-20261121-101500-a1b2c3 sta")
	io.WriteString(w, "rting\nsome other line\ncc-guestinit: review finished: exit 0\n")
	if want := []State{StateRunning, StateFinished}; !slices.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
	// Another run's id in the log changes nothing.
	got = nil
	io.WriteString(w, "cc-guestinit: review cc-20991231-000000-zzzzzz starting\n")
	if len(got) != 0 {
		t.Errorf("a foreign run id moved the state: %v", got)
	}
}
