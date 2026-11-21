package review

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
)

// SummaryVersion changes whenever summary.json's meaning does.
const SummaryVersion = 1

// Outcome sums up a run in a word.
type Outcome string

// The outcomes.
const (
	OutcomeOK         Outcome = "ok"
	OutcomeBudget     Outcome = "budget"     // stopped at --max-budget-usd
	OutcomeRefused    Outcome = "refused"    // the model looks to have refused
	OutcomeError      Outcome = "error"      // Claude Code reported an error
	OutcomeIncomplete Outcome = "incomplete" // the job never finished
	OutcomeFailed     Outcome = "failed"     // a selftest probe got out
)

// Result holds the fields of Claude Code's final stream-json event.
type Result struct {
	Subtype           string  `json:"subtype"`
	IsError           bool    `json:"is_error"`
	CostUSD           float64 `json:"total_cost_usd"`
	NumTurns          int     `json:"num_turns"`
	DurationMS        int     `json:"duration_ms"`
	PermissionDenials int     `json:"permission_denials"`
}

// Probes counts a selftest's verdicts.
type Probes struct {
	Passed    int      `json:"passed"`
	Failed    int      `json:"failed"`
	FailedIDs []string `json:"failed_ids,omitempty"`
}

// Summary goes to summary.json beside report.md.
type Summary struct {
	Version      int       `json:"version"`
	RunID        string    `json:"run_id"`
	Mode         string    `json:"mode"`
	Target       string    `json:"target,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Base         string    `json:"base,omitempty"`
	Head         string    `json:"head,omitempty"`
	Started      time.Time `json:"started"`
	Finished     time.Time `json:"finished"`
	Outcome      Outcome   `json:"outcome"`
	ExitCode     *int      `json:"exit_code"`
	Result       Result    `json:"result"`
	SubAgents    int       `json:"sub_agents"`
	Refused      bool      `json:"refused"`
	ProxyDenials int       `json:"proxy_denials"`
	DeniedHosts  []string  `json:"denied_hosts,omitempty"`
	Probes       *Probes   `json:"probes,omitempty"`
	Warnings     []string  `json:"warnings,omitempty"`
}

// refusalPatterns flag a result that reads like a refusal; a flag, not proof.
var refusalPatterns = regexp.MustCompile(`(?i)\bI (can't|cannot|won't|will not|am unable to|'m unable to|'m not able to) (help|assist|comply|review|do that)\b|against my (guidelines|policies)`)

// maxStream caps how much of the transcript Analyse reads.
const maxStream = 200 << 20

// Analyse reads the files cc-launch copied back and works out the outcome,
// the summary and the report text. Missing files mean the job didn't finish;
// that shows as an outcome, not an error.
func Analyse(dir string, mode guestinit.Mode) (Summary, string, error) {
	s := Summary{Version: SummaryVersion, Mode: string(mode)}
	if b, err := os.ReadFile(filepath.Join(dir, "exit-code")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			s.ExitCode = &n
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, "", err
	}
	stream, err := readCapped(filepath.Join(dir, "stream.jsonl"), maxStream)
	if err != nil {
		return s, "", err
	}
	if err := s.countDenials(filepath.Join(dir, "proxy-denials.jsonl")); err != nil {
		return s, "", err
	}

	var report string
	if mode == guestinit.ModeSelftest {
		report = s.analyseSelftest(stream)
	} else {
		report = s.analyseReview(stream)
	}
	if s.ExitCode == nil {
		s.warn("No exit code came back: the VM ended before the job finished.")
	} else if *s.ExitCode != 0 && s.Outcome == OutcomeOK {
		s.warn(fmt.Sprintf("The job exited with code %d.", *s.ExitCode))
	}
	if s.ProxyDenials > 0 {
		s.warn(fmt.Sprintf("The proxy refused %d request(s) from the VM (%s); see proxy-denials.jsonl.",
			s.ProxyDenials, strings.Join(s.DeniedHosts, ", ")))
	}
	return s, report, nil
}

func (s *Summary) warn(w string) { s.Warnings = append(s.Warnings, w) }

// analyseReview finds the result event and sub-agent calls in the
// stream-json transcript.
func (s *Summary) analyseReview(stream []byte) string {
	var text string
	found := false
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type              string            `json:"type"`
			Subtype           string            `json:"subtype"`
			IsError           bool              `json:"is_error"`
			CostUSD           float64           `json:"total_cost_usd"`
			NumTurns          int               `json:"num_turns"`
			DurationMS        int               `json:"duration_ms"`
			Result            string            `json:"result"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
			Message           struct {
				Content []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" && (c.Name == "Task" || c.Name == "Agent") {
					s.SubAgents++
				}
			}
		case "result":
			found = true
			s.Result = Result{Subtype: ev.Subtype, IsError: ev.IsError, CostUSD: ev.CostUSD,
				NumTurns: ev.NumTurns, DurationMS: ev.DurationMS, PermissionDenials: len(ev.PermissionDenials)}
			text = ev.Result
		}
	}

	switch {
	case !found:
		s.Outcome = OutcomeIncomplete
		s.warn("The transcript holds no result: the review never finished.")
		return "# No report\n\nThe review ended before Claude Code wrote a result. " +
			"See stderr.log and the console output for why.\n"
	case s.Result.Subtype == "error_max_budget_usd":
		s.Outcome = OutcomeBudget
		s.warn(fmt.Sprintf("The review stopped at its budget (US$%.2f spent).", s.Result.CostUSD))
		if text == "" {
			text = "# Stopped at the budget\n\nThe review reached its budget before it finished. " +
				"Run it again with a larger --budget.\n"
		}
	case s.Result.IsError || s.Result.Subtype != "success":
		s.Outcome = OutcomeError
		s.warn("Claude Code reported an error: " + s.Result.Subtype + ".")
	case refusalPatterns.MatchString(text):
		s.Outcome, s.Refused = OutcomeRefused, true
		s.warn("The result reads like a refusal; check report.md.")
	default:
		s.Outcome = OutcomeOK
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// analyseSelftest counts cc-selftest's PASS and FAIL lines; without its
// SUMMARY line the probes never finished.
func (s *Summary) analyseSelftest(stream []byte) string {
	p := &Probes{}
	s.Probes = p
	summary := false
	var lines []string
	for _, line := range strings.Split(string(stream), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "PASS":
			p.Passed++
		case "FAIL":
			p.Failed++
			p.FailedIDs = append(p.FailedIDs, f[1])
		case "SUMMARY":
			summary = true
		default:
			continue
		}
		lines = append(lines, line)
	}
	switch {
	case !summary:
		s.Outcome = OutcomeIncomplete
		s.warn("The selftest never reached its summary.")
	case p.Failed > 0:
		s.Outcome = OutcomeFailed
		s.warn(fmt.Sprintf("%d probe(s) got out of the sandbox: %s.", p.Failed, strings.Join(p.FailedIDs, ", ")))
	default:
		s.Outcome = OutcomeOK
	}
	return "# Selftest\n\n```\n" + strings.Join(lines, "\n") + "\n```\n"
}

// countDenials reads cc-launch's copy of the run's proxy denials.
func (s *Summary) countDenials(path string) error {
	b, err := readCapped(path, 4<<20)
	if err != nil {
		return err
	}
	hosts := map[string]bool{}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var d struct {
			Host string `json:"host"`
		}
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &d) != nil {
			continue
		}
		s.ProxyDenials++
		if d.Host != "" {
			hosts[d.Host] = true
		}
	}
	for h := range hosts {
		s.DeniedHosts = append(s.DeniedHosts, h)
	}
	slices.Sort(s.DeniedHosts)
	if len(s.DeniedHosts) > 10 {
		s.DeniedHosts = s.DeniedHosts[:10]
	}
	return nil
}

// readCapped reads a file up to max bytes; a missing file reads as empty.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// WriteReport writes report.md and summary.json into dir, which it creates
// with mode 0700: a report can quote the code under review.
func WriteReport(dir string, s Summary, report string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	js, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	for name, b := range map[string][]byte{"report.md": []byte(report), "summary.json": append(js, '\n')} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}
