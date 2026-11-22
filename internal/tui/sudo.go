package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Handover runs the interactive privilege login (spec §2): the interface
// gives the terminal to the privilege tool so its password prompt works as
// usual, then keeps the login fresh while root steps run.
type Handover struct {
	// Exec hands the terminal over; cb turns the login's result into a
	// message. Production uses tea.ExecProcess.
	Exec func(cb func(error) tea.Msg) tea.Cmd
	// KeepAlive refreshes the login until ctx ends; nil skips it.
	KeepAlive func(ctx context.Context) <-chan error
}

// loginPrompt replaces sudo's prompt, so the user knows who asks.
const loginPrompt = "codecheck-sandbox setup needs root for the next step. Password for %u: "

// LoginCommand builds the command that asks for the password. The engine
// runs every root command with -n afterwards, so this one stays the only
// prompt.
func LoginCommand(tool system.Tool) *exec.Cmd {
	switch tool.Kind {
	case system.ToolSudo, system.ToolSudoRS:
		return exec.Command(tool.Path, "-v", "-p", loginPrompt)
	}
	// doas keeps a login with "persist"; run0 asks polkit, which may keep
	// one too. Either way, a harmless command does the asking.
	return exec.Command(tool.Path, "true")
}

// NewHandover builds the production handover for tool.
func NewHandover(tool system.Tool, r system.Runner) *Handover {
	return &Handover{
		Exec: func(cb func(error) tea.Msg) tea.Cmd {
			return tea.ExecProcess(LoginCommand(tool), tea.ExecCallback(cb))
		},
		KeepAlive: func(ctx context.Context) <-chan error {
			return system.StartKeepAlive(ctx, r, tool)
		},
	}
}

// ErrLoginFailed wraps a privilege login that didn't succeed: a wrong
// password, or Ctrl-C at the prompt.
var ErrLoginFailed = errors.New("the privilege login failed")

// elevateReq carries one engine request for the privilege login.
type elevateReq struct{ reply chan error }

type loginDoneMsg struct {
	err error
	req elevateReq
}

// elevate implements step.ElevateFunc.
func (a *App) elevate(ctx context.Context) error {
	req := elevateReq{reply: make(chan error, 1)}
	select {
	case a.requests <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) startLogin(req elevateReq) tea.Cmd {
	a.elevating = true
	return a.cfg.Handover.Exec(func(err error) tea.Msg { return loginDoneMsg{err: err, req: req} })
}

// loginDone answers the engine. A good login (re)starts the keep-alive; a
// lapse that ends the keep-alive surfaces as ErrLoginExpired on the next
// root command, and the engine asks for another handover.
func (a *App) loginDone(msg loginDoneMsg) {
	a.elevating = false
	if msg.err == nil && a.cfg.Handover.KeepAlive != nil {
		if a.stopKeepAlive != nil {
			a.stopKeepAlive()
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.stopKeepAlive = cancel
		errs := a.cfg.Handover.KeepAlive(ctx)
		go func() { // drain; the engine notices a lapse itself
			for {
				select {
				case _, ok := <-errs:
					if !ok {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	if msg.err != nil {
		msg.err = fmt.Errorf("%w: %w", ErrLoginFailed, msg.err)
	}
	msg.req.reply <- msg.err
}
