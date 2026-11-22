package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// --- doctor ---

func TestDoctorTable(t *testing.T) {
	rows := []DoctorRow{
		{Name: "KVM access", OK: true, Detail: "/dev/kvm readable and writable by fcjail"},
		{Name: "Firewall rules", OK: false, Detail: "rule hash differs from the installed rules; run setup"},
		{Name: "Egress proxy", OK: true, Detail: "listening on 172.16.0.1:3180 as codecheck-proxy"},
	}
	s := stripANSI(DoctorView(theme.New(true), rows, 100))
	checkSize(t, s)
	golden(t, "doctor", s)
	if !strings.Contains(s, "2 passed, 1 failed") {
		t.Errorf("no tally:\n%s", s)
	}
	long := append(rows, DoctorRow{Name: "Guest image", Detail: strings.Repeat("hash mismatch ", 20)})
	checkSize(t, stripANSI(DoctorView(theme.New(true), long, 100)))
}

// --- review ---

// reviewRun stands in for review.Pipeline: it reports each state, then
// returns a report folder holding report.md.
func reviewRun(t *testing.T, report string, gate <-chan struct{}) ReviewFunc {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.md"), []byte(report), 0o600)
	return func(ctx context.Context, status func(review.Status)) (review.RunResult, error) {
		started := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
		for _, s := range []review.State{review.StatePreparing, review.StateBooting, review.StateRunning} {
			status(review.Status{State: s, Started: started})
		}
		if gate != nil {
			<-gate
		}
		status(review.Status{State: review.StateDone, Started: started})
		cost := review.Result{CostUSD: 3.4123}
		return review.RunResult{ReportDir: dir, Summary: review.Summary{
			Outcome: review.OutcomeOK, Result: cost, SubAgents: 4, ProxyDenials: 2,
			Warnings: []string{"The proxy refused 2 request(s) from the VM (github.com); see proxy-denials.jsonl."},
		}}, nil
	}
}

func newReviewAt(run ReviewFunc) *Review {
	r := NewReview(theme.New(true), "github.com/acme/widget (PR 42)", run)
	r.now = func() time.Time { return time.Date(2026, 11, 22, 10, 7, 5, 0, time.UTC) }
	return r
}

func TestReviewShowsLiveStatus(t *testing.T) {
	gate := make(chan struct{})
	h := start(t, newReviewAt(reviewRun(t, "# Findings\n", gate)))
	r := h.m.(*Review)
	waitFor(t, h, func() bool { return r.state == review.StateRunning })
	r.spin = newSpinner(r.th)
	s := h.screen()
	checkSize(t, s)
	golden(t, "review-running", s)
	if !strings.Contains(s, "7m05s") || !strings.Contains(s, "running") {
		t.Errorf("no elapsed time or state:\n%s", s)
	}
	close(gate)
	waitFor(t, h, func() bool { return r.done })
}

func TestReviewShowsTheReport(t *testing.T) {
	h := start(t, newReviewAt(reviewRun(t, "# Findings\n\n## SQL injection in `search`\n\nThe query joins user input.\n", nil)))
	r := h.m.(*Review)
	waitFor(t, h, func() bool { return r.done })
	r.result.ReportDir = "/home/user/codecheck-reports/widget-pr42-20261122-100000" // the temp folder varies
	s := h.screen()
	checkSize(t, s)
	golden(t, "review-report", s)
	for _, want := range []string{"SQL injection", "US$3.41", "4 sub-agents", "proxy refused 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("report screen lacks %q", want)
		}
	}
	h.press("q")
	h.untilQuit()
}

// A report quotes code the model read, so it may carry escape sequences
// meant for the terminal; none may reach it.
func TestReviewStripsTerminalEscapesFromTheReport(t *testing.T) {
	evil := "# Findings\n\nclip\x1b]52;c;ZXZpbA==\x07 title\x1b]0;pwned\x07 colour\x1b[31mred\x9b31m bell\a\r\n"
	h := start(t, newReviewAt(reviewRun(t, evil, nil)))
	r := h.m.(*Review)
	waitFor(t, h, func() bool { return r.done })
	raw := r.View().Content
	for _, bad := range []string{"\x1b]", "\a", "\x9b", "pwned\x07"} {
		if strings.Contains(raw, bad) {
			t.Errorf("the view holds %q from the report", bad)
		}
	}
	if !strings.Contains(stripANSI(raw), "clip") {
		t.Error("sanitising dropped the text around the escapes")
	}
}

func TestSanitise(t *testing.T) {
	// Whole sequences go, payload and all; then stray controls, invalid
	// bytes and bidirectional overrides (which can hide text).
	in := "a\x1b[2Jb\x1b]8;;http://x\x07c\td\x00e‮f\r\ng\x9bh\u0085i"
	if got := sanitise(in); got != "abc\tdef\nghi" {
		t.Errorf("sanitise = %q", got)
	}
}

func TestReviewReportsAFailure(t *testing.T) {
	h := start(t, newReviewAt(func(ctx context.Context, status func(review.Status)) (review.RunResult, error) {
		status(review.Status{State: review.StatePreparing, Started: time.Now()})
		return review.RunResult{}, errors.New("cloning: repository not found")
	}))
	r := h.m.(*Review)
	waitFor(t, h, func() bool { return r.done })
	if s := h.screen(); !strings.Contains(s, "repository not found") {
		t.Errorf("the failure doesn't show:\n%s", s)
	}
	if r.Err() == nil {
		t.Error("Err lost the failure")
	}
}

func TestReviewQuitStopsTheRun(t *testing.T) {
	stopped := make(chan struct{})
	h := start(t, newReviewAt(func(ctx context.Context, status func(review.Status)) (review.RunResult, error) {
		status(review.Status{State: review.StateRunning, Started: time.Now()})
		<-ctx.Done()
		close(stopped)
		return review.RunResult{}, ctx.Err()
	}))
	r := h.m.(*Review)
	waitFor(t, h, func() bool { return r.state == review.StateRunning })
	h.press("q")
	if h.quit || !r.confirmingQuit {
		t.Fatal("q quit a running review without asking")
	}
	h.press("y")
	<-stopped
}

// waitFor feeds messages until cond holds, for models other than App.
func waitFor(t *testing.T, h *harness, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case msg := <-h.msgs:
			h.update(msg)
		case <-deadline:
			t.Fatalf("timed out; screen:\n%s", h.screen())
		}
	}
}

// --- uninstall ---

var installed = []step.Item{
	{ID: "host-jail-user", Kind: step.KindUser, Name: "fcjail", State: step.Created},
	{ID: "host-rules", Kind: step.KindFile, Path: "/etc/codecheck-sandbox/rules.nft", State: step.Created},
	{ID: "host-tap-service", Kind: step.KindUnit, Name: "codecheck-tap.service", State: step.Created},
	{ID: "components", Kind: step.KindPackage, Name: "e2fsprogs", State: step.Adopted},
}

func TestUninstallList(t *testing.T) {
	u := NewUninstall(theme.New(true), installed, "~/codecheck-reports", false)
	u.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s := stripANSI(u.View().Content)
	checkSize(t, s)
	golden(t, "uninstall", s)
	removes, keeps, _ := strings.Cut(s, "These stay")
	if !strings.Contains(removes, "fcjail") || strings.Contains(removes, "e2fsprogs") {
		t.Errorf("the remove list came out wrong:\n%s", removes)
	}
	if !strings.Contains(keeps, "e2fsprogs") || !strings.Contains(keeps, "~/codecheck-reports") {
		t.Errorf("the keep list came out wrong:\n%s", keeps)
	}
}

func TestUninstallAsks(t *testing.T) {
	u := NewUninstall(theme.New(true), installed, "~/codecheck-reports", false)
	u.Update(keyMsg("q"))
	if !u.Done() || u.Confirmed() {
		t.Error("q should cancel")
	}
	u = NewUninstall(theme.New(true), installed, "~/codecheck-reports", true)
	if s := stripANSI(u.View().Content); !strings.Contains(s, "codecheck-reports") || strings.Contains(s, "These stay:\n  ~/codecheck") {
		t.Errorf("--purge should list the reports as going:\n%s", s)
	}
	u.Update(keyMsg("enter"))
	if !u.Done() || !u.Confirmed() {
		t.Error("Enter should confirm")
	}
}
