package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
)

const (
	initLine     = `{"type":"system","subtype":"init","tools":["Task","Glob","Grep","Read","Skill"]}`
	subAgentLine = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","input":{}},{"type":"text","text":"hunting"}]}}`
	agentLine    = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","input":{}}]}}`
	readLine     = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`
)

func resultLine(subtype string, isError bool, cost float64, text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": subtype, "is_error": isError, "total_cost_usd": cost,
		"num_turns": 12, "duration_ms": 61000, "result": text,
		"permission_denials": []any{map[string]any{"tool_name": "Bash"}},
	})
	return string(b)
}

// resultDir lays out what cc-launch copies back.
func resultDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	return dir
}

func TestAnalyseSuccess(t *testing.T) {
	dir := resultDir(t, map[string]string{
		"stream.jsonl":        strings.Join([]string{initLine, subAgentLine, agentLine, readLine, resultLine("success", false, 1.2345, "# Findings\n\nNone.")}, "\n") + "\n",
		"stderr.log":          "",
		"exit-code":           "0\n",
		"proxy-denials.jsonl": "",
	})
	s, report, err := Analyse(dir, guestinit.ModeReview)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	if s.Outcome != OutcomeOK || s.Result.Subtype != "success" || s.Result.CostUSD != 1.2345 || s.SubAgents != 2 {
		t.Errorf("summary = %+v", s)
	}
	if s.ExitCode == nil || *s.ExitCode != 0 || s.Result.PermissionDenials != 1 || s.Result.NumTurns != 12 {
		t.Errorf("summary = %+v", s)
	}
	if report != "# Findings\n\nNone.\n" {
		t.Errorf("report = %q", report)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings = %v", s.Warnings)
	}
}

func TestAnalyseBudgetStop(t *testing.T) {
	dir := resultDir(t, map[string]string{
		"stream.jsonl": initLine + "\n" + resultLine("error_max_budget_usd", true, 25.01, "") + "\n",
		"exit-code":    "1\n",
	})
	s, report, _ := Analyse(dir, guestinit.ModeReview)
	if s.Outcome != OutcomeBudget || !hasWarning(s, "budget") {
		t.Errorf("summary = %+v", s)
	}
	if !strings.Contains(report, "budget") {
		t.Errorf("report doesn't explain the stop: %q", report)
	}
}

func TestAnalyseRefusal(t *testing.T) {
	dir := resultDir(t, map[string]string{
		"stream.jsonl": resultLine("success", false, 0.1, "I can't help with reviewing this code.") + "\n",
		"exit-code":    "0\n",
	})
	s, _, _ := Analyse(dir, guestinit.ModeReview)
	if s.Outcome != OutcomeRefused || !s.Refused || !hasWarning(s, "refus") {
		t.Errorf("summary = %+v", s)
	}
}

func TestAnalyseEmpty(t *testing.T) {
	// The VM stopped before the job wrote anything.
	s, report, err := Analyse(resultDir(t, nil), guestinit.ModeReview)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	if s.Outcome != OutcomeIncomplete || s.ExitCode != nil || !hasWarning(s, "exit code") {
		t.Errorf("summary = %+v", s)
	}
	if report == "" {
		t.Error("an empty run left no report explaining it")
	}
}

func TestAnalyseNoResultEvent(t *testing.T) {
	dir := resultDir(t, map[string]string{"stream.jsonl": initLine + "\n" + `{"type":"assistant"` + "\n", "exit-code": "137\n"})
	s, _, _ := Analyse(dir, guestinit.ModeReview)
	if s.Outcome != OutcomeIncomplete || *s.ExitCode != 137 {
		t.Errorf("summary = %+v", s)
	}
}

func TestAnalyseProxyDenials(t *testing.T) {
	dir := resultDir(t, map[string]string{
		"stream.jsonl": resultLine("success", false, 0.5, "ok") + "\n",
		"exit-code":    "0\n",
		"proxy-denials.jsonl": `{"time":"2026-11-21T10:00:00Z","reason":"denied-host","host":"github.com"}` + "\n" +
			`{"time":"2026-11-21T10:00:01Z","reason":"denied-host","host":"github.com"}` + "\n" +
			`{"time":"2026-11-21T10:00:02Z","reason":"denied-path","host":"api.anthropic.com","path":"/v1/files"}` + "\n",
	})
	s, _, _ := Analyse(dir, guestinit.ModeReview)
	if s.ProxyDenials != 3 || !slices.Equal(s.DeniedHosts, []string{"api.anthropic.com", "github.com"}) || !hasWarning(s, "proxy") {
		t.Errorf("summary = %+v", s)
	}
	if s.Outcome != OutcomeOK {
		t.Errorf("denials alone changed the outcome to %s", s.Outcome)
	}
}

func TestAnalyseSelftest(t *testing.T) {
	pass := "PASS no-dns no name lookups\nPASS proxy-allows-anthropic the proxy tunnels\nSUMMARY 2 passed, 0 failed\n"
	s, _, _ := Analyse(resultDir(t, map[string]string{"stream.jsonl": pass, "exit-code": "0\n"}), guestinit.ModeSelftest)
	if s.Outcome != OutcomeOK || s.Probes.Passed != 2 || s.Probes.Failed != 0 {
		t.Errorf("passing selftest = %+v", s)
	}

	fail := "PASS no-dns no name lookups\nFAIL host-port-22 no connection to host port 22\nSUMMARY 1 passed, 1 failed\n"
	s, report, _ := Analyse(resultDir(t, map[string]string{"stream.jsonl": fail, "exit-code": "1\n"}), guestinit.ModeSelftest)
	if s.Outcome != OutcomeFailed || !slices.Equal(s.Probes.FailedIDs, []string{"host-port-22"}) {
		t.Errorf("failing selftest = %+v", s)
	}
	if !strings.Contains(report, "host-port-22") {
		t.Errorf("report = %q", report)
	}

	// No SUMMARY line: the probes never finished.
	s, _, _ = Analyse(resultDir(t, map[string]string{"stream.jsonl": "PASS no-dns x\n", "exit-code": "0\n"}), guestinit.ModeSelftest)
	if s.Outcome != OutcomeIncomplete {
		t.Errorf("unfinished selftest = %+v", s)
	}
}

func TestWriteReport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report")
	s := Summary{Version: SummaryVersion, RunID: "cc-20261121-101500-a1b2c3", Outcome: OutcomeOK}
	if err := WriteReport(dir, s, "# Findings\n"); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("report folder mode %v, want 0700", fi.Mode().Perm())
	}
	for _, name := range []string{"report.md", "summary.json"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", name, fi, err)
		}
	}
	var back Summary
	b, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err := json.Unmarshal(b, &back); err != nil || back.RunID != s.RunID || back.Outcome != OutcomeOK {
		t.Errorf("summary.json = %s (%v)", b, err)
	}
}

func hasWarning(s Summary, word string) bool {
	for _, w := range s.Warnings {
		if strings.Contains(strings.ToLower(w), word) {
			return true
		}
	}
	return false
}
