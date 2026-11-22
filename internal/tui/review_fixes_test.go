package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
)

// The real host phase holds 15 steps; a short terminal must cut the list,
// not crash.
func TestLongPhaseInAShortTerminal(t *testing.T) {
	var steps []StepInfo
	for i := range 15 {
		steps = append(steps, StepInfo{ID: fmt.Sprintf("host-%d", i), Title: fmt.Sprintf("Host step %d", i), Phase: step.PhaseHost})
	}
	cfg := testConfig(nil)
	cfg.Steps = steps
	for _, height := range []int{20, 12, 6} {
		a := New(cfg)
		a.current = "host-14"
		a.states["host-14"] = running
		a.Update(tea.WindowSizeMsg{Width: 100, Height: height})
		a.openConfirm(confirmReq{id: "host-14", title: "Host step 14", plan: tapPlan, reply: make(chan bool, 1)})
		for _, finished := range []bool{false, true} {
			a.finished = finished
			if finished {
				a.confirm, a.err = nil, errors.New("boom")
			}
			s := stripANSI(a.View().Content)
			if n := len(strings.Split(s, "\n")); n > height {
				t.Errorf("height %d: %d lines", height, n)
			}
			if height == 20 && !strings.Contains(s, "Host step 14") {
				t.Errorf("height %d: the running step fell off the list:\n%s", height, s)
			}
		}
	}
}

// A network failure must not stick: the next Enter tries again. A firm
// answer (accepted, or rejected by Anthropic) still gets remembered.
func TestKeyValidatorRetriesPassingFailures(t *testing.T) {
	var calls atomic.Int32
	var online atomic.Bool
	v := keyValidator(func(ctx context.Context, key string) error {
		calls.Add(1)
		if !online.Load() {
			return errors.New("couldn't reach Anthropic")
		}
		return nil
	})
	if v(goodKey) == nil {
		t.Fatal("the network failure got lost")
	}
	online.Store(true)
	if err := v(goodKey); err != nil || calls.Load() != 2 {
		t.Errorf("after the network came back: %v, %d calls", err, calls.Load())
	}

	calls.Store(0)
	rejected := keyValidator(func(ctx context.Context, key string) error {
		calls.Add(1)
		return fmt.Errorf("%w (401)", ErrKeyRejected)
	})
	rejected(goodKey)
	rejected(goodKey)
	if calls.Load() != 1 {
		t.Errorf("a firm rejection got checked %d times", calls.Load())
	}
}

func TestKeyCheckerMarksFirmRejections(t *testing.T) {
	for status, firm := range map[int]bool{401: true, 403: true, 429: false, 500: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		err := KeyChecker{BaseURL: srv.URL, Client: srv.Client()}.Check(context.Background(), goodKey)
		srv.Close()
		if errors.Is(err, ErrKeyRejected) != firm {
			t.Errorf("%d: %v; firm %v", status, err, firm)
		}
	}
}

func TestConsentCanBeAbandoned(t *testing.T) {
	for _, k := range []string{"ctrl+c", "esc", "q"} {
		c := newConsentAt(100, 30)
		send(c, "y")
		_, cmd := c.Update(keyMsg(k))
		if !c.Done() || !c.Aborted() || c.Choice() || cmd == nil {
			t.Errorf("%s: done %v, aborted %v, choice %v, quit %v", k, c.Done(), c.Aborted(), c.Choice(), cmd != nil)
		}
	}
}

func TestConsentRightPicksYes(t *testing.T) {
	c := newConsentAt(100, 30)
	send(c, "right", "right")
	if !c.Choice() {
		t.Error("→ twice landed on No")
	}
	send(c, "left", "left")
	if c.Choice() {
		t.Error("← twice landed on Yes")
	}
	send(c, "tab")
	if !c.Choice() {
		t.Error("tab should still switch")
	}
}

// The login must not stay fresh after the run: the keep-alive stops when
// the engine finishes, not only on quit.
func TestKeepAliveStopsWhenTheRunEnds(t *testing.T) {
	fake := &fakeHandover{}
	cfg := testConfig(func(ctx context.Context, opts step.Options) error {
		return opts.Elevate(ctx)
	})
	cfg.Handover = fake.handover()
	a := New(cfg)
	h := &harness{t: t, m: a, msgs: make(chan tea.Msg, 256)}
	h.run(a.Init())
	h.until("the end", func(a *App) bool { return a.Finished() })
	if ka := fake.alive(); len(ka) != 1 || ka[0].Err() == nil {
		t.Errorf("keep-alive after the run: %d started, stopped %v", len(ka), len(ka) == 1 && ka[0].Err() != nil)
	}
	a.Stop()
}

func TestReviewTimeStopsWithTheRun(t *testing.T) {
	clock := time.Date(2026, 11, 22, 10, 12, 0, 0, time.UTC)
	r := NewReview(theme.New(true), "x", nil)
	r.now = func() time.Time { return clock }
	r.Update(review.Status{State: review.StateRunning, Started: clock.Add(-12 * time.Minute)})
	r.Update(reviewDoneMsg{result: review.RunResult{}})
	clock = clock.Add(5 * time.Minute) // the user reads for five minutes
	if s := stripANSI(r.View().Content); !strings.Contains(s, "12m00s") {
		t.Errorf("the finished time moved on:\n%s", s)
	}
}

func TestReviewTimeBeforeTheFirstStatus(t *testing.T) {
	r := NewReview(theme.New(true), "x", nil)
	if s := stripANSI(r.View().Content); !strings.Contains(s, "0m00s") || strings.Contains(s, "h") && strings.Contains(s, "2562047") {
		t.Errorf("before any status:\n%s", s)
	}
	r.Update(reviewDoneMsg{err: errors.New("cloning failed")})
	if s := stripANSI(r.View().Content); strings.Contains(s, "2562047") {
		t.Errorf("a failure before any status:\n%s", s)
	}
}

func TestPlainSaysNeedsYesOnce(t *testing.T) {
	var out bytes.Buffer
	RunPlain(context.Background(), PlainConfig{Out: &out, Steps: setupSteps, Run: fakeEngine(nil)})
	if n := strings.Count(out.String(), "--yes"); n != 1 {
		t.Errorf("--yes appears %d times:\n%s", n, out.String())
	}
}
