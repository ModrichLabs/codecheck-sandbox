package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// realSandbox runs the review pipeline against the installed sandbox:
// cc-launch through the privilege tool and its sudoers rule.
type realSandbox struct {
	env  Env
	tool system.Tool
	priv system.PrivRunner
}

func newSandbox(env Env, _ Options) (Sandbox, error) {
	tool, err := system.DetectTool(context.Background(), system.ExecRunner{}, exec.LookPath)
	if err != nil {
		return nil, err
	}
	return &realSandbox{env: env, tool: tool, priv: system.PrivRunner{Tool: tool, Inner: system.ExecRunner{}}}, nil
}

// pipeline builds the review pipeline for one run.
func (s *realSandbox) pipeline(status func(review.Status), console io.Writer) (*review.Pipeline, error) {
	c, _, err := s.env.LoadConfig()
	if err != nil {
		return nil, err
	}
	reports, err := c.ReportsPath(s.env.Home)
	if err != nil {
		return nil, err
	}
	priv := s.priv
	return &review.Pipeline{
		Run: system.ExecRunner{}, Root: priv, Paths: launch.DefaultPaths(),
		WorkRoot: filepath.Join(s.env.Home, "codecheck-work"), ReportsRoot: reports, Now: time.Now,
		Launcher: func(console io.Writer) review.Launcher { return launch.Client{Priv: priv, Console: console} },
		Status:   status, Console: console,
	}, nil
}

// login asks for the password first where the sudoers rule can't help:
// doas and run0 never read it (M9), so their -n would fail. sudo and sudo-rs
// run cc-launch without a password through the rule.
func (s *realSandbox) login(ctx context.Context) (stop func(), err error) {
	if s.tool.Kind == system.ToolSudo || s.tool.Kind == system.ToolSudoRS {
		return func() {}, nil
	}
	if !s.env.TTY {
		return nil, fmt.Errorf("%s asks for a password each run; run this in a terminal", s.tool.Kind)
	}
	cmd := tui.LoginCommand(s.tool)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %w", tui.ErrLoginFailed, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	system.StartKeepAlive(ctx, system.ExecRunner{}, s.tool)
	return cancel, nil
}

func (s *realSandbox) Review(ctx context.Context, o review.Options, status func(review.Status), console io.Writer) (review.RunResult, error) {
	p, err := s.pipeline(status, console)
	if err != nil {
		return review.RunResult{}, err
	}
	stop, err := s.login(ctx)
	if err != nil {
		return review.RunResult{}, err
	}
	defer stop()
	return p.Review(ctx, o)
}

func (s *realSandbox) Selftest(ctx context.Context, ports []int, status func(review.Status), console io.Writer) (review.RunResult, error) {
	p, err := s.pipeline(status, console)
	if err != nil {
		return review.RunResult{}, err
	}
	stop, err := s.login(ctx)
	if err != nil {
		return review.RunResult{}, err
	}
	defer stop()
	return p.Selftest(ctx, ports)
}
