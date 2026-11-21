package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeHandover stands in for tea.ExecProcess(sudo -v) and the keep-alive.
type fakeHandover struct {
	mu        sync.Mutex
	logins    int
	loginErr  error
	gate      chan struct{} // when set, the login waits for it
	keepalive []context.Context
}

func (f *fakeHandover) handover() *Handover {
	return &Handover{
		Exec: func(cb func(error) tea.Msg) tea.Cmd {
			return func() tea.Msg {
				if f.gate != nil {
					<-f.gate
				}
				f.mu.Lock()
				f.logins++
				err := f.loginErr
				f.mu.Unlock()
				return cb(err)
			}
		},
		KeepAlive: func(ctx context.Context) <-chan error {
			f.mu.Lock()
			f.keepalive = append(f.keepalive, ctx)
			f.mu.Unlock()
			return make(chan error)
		},
	}
}

func (f *fakeHandover) alive() []context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keepalive)
}

func TestElevateHandsOverThenKeepsTheLoginAlive(t *testing.T) {
	fake := &fakeHandover{gate: make(chan struct{})}
	results := make(chan error, 2)
	cfg := testConfig(func(ctx context.Context, opts step.Options) error {
		hostSoFar(ctx, opts)
		results <- opts.Elevate(ctx)
		results <- opts.Elevate(ctx) // the login lapsed: a second handover
		<-ctx.Done()
		return ctx.Err()
	})
	cfg.Handover = fake.handover()
	h := start(t, New(cfg))

	h.until("the handover", func(a *App) bool { return a.elevating })
	if s := h.screen(); !strings.Contains(s, "password") {
		t.Errorf("no word about the password prompt:\n%s", s)
	}
	close(fake.gate)
	h.until("both logins", func(*App) bool { return len(fake.alive()) == 2 })
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Elevate: %v", err)
		}
	}
	ka := fake.alive()
	if ka[0].Err() == nil {
		t.Error("the first keep-alive still runs beside the second")
	}
	if ka[1].Err() != nil {
		t.Error("the current keep-alive stopped early")
	}
	h.press("q")
	h.untilQuit()
	if ka[1].Err() == nil {
		t.Error("quitting left the keep-alive running")
	}
}

func TestElevateReportsAFailedLogin(t *testing.T) {
	fake := &fakeHandover{loginErr: errors.New("sudo: 3 incorrect password attempts")}
	result := make(chan error, 1)
	cfg := testConfig(func(ctx context.Context, opts step.Options) error {
		err := opts.Elevate(ctx)
		result <- err
		return err
	})
	cfg.Handover = fake.handover()
	h := start(t, New(cfg))
	h.until("the run to end", func(a *App) bool { return a.Finished() })
	err := <-result
	if !errors.Is(err, ErrLoginFailed) || !strings.Contains(err.Error(), "incorrect password") {
		t.Errorf("Elevate = %v", err)
	}
	if !strings.Contains(Fix(err), "password") {
		t.Errorf("fix for a failed login: %q", Fix(err))
	}
	if len(fake.alive()) != 0 {
		t.Error("a keep-alive started after a failed login")
	}
}

func TestNoHandoverLeavesElevateUnset(t *testing.T) {
	got := make(chan bool, 1)
	h := start(t, New(testConfig(func(ctx context.Context, opts step.Options) error {
		got <- opts.Elevate == nil
		return nil
	})))
	h.until("the end", func(a *App) bool { return a.Finished() })
	if !<-got {
		t.Error("Elevate set without a handover")
	}
}

func TestLoginCommand(t *testing.T) {
	for _, c := range []struct {
		tool system.Tool
		want []string
	}{
		{system.Tool{Kind: system.ToolSudo, Path: "/usr/bin/sudo"}, []string{"/usr/bin/sudo", "-v", "-p", loginPrompt}},
		{system.Tool{Kind: system.ToolSudoRS, Path: "/usr/bin/sudo-rs"}, []string{"/usr/bin/sudo-rs", "-v", "-p", loginPrompt}},
		{system.Tool{Kind: system.ToolDoas, Path: "/usr/bin/doas"}, []string{"/usr/bin/doas", "true"}},
		{system.Tool{Kind: system.ToolRun0, Path: "/usr/bin/run0"}, []string{"/usr/bin/run0", "true"}},
	} {
		if got := LoginCommand(c.tool).Args; !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.tool.Kind, got, c.want)
		}
	}
}
