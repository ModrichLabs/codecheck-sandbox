package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ToolKind names a supported privilege tool.
type ToolKind string

// The privilege tools the engine knows. Every one of them accepts -n, which
// fails at once instead of prompting for a password.
const (
	ToolSudo   ToolKind = "sudo"
	ToolSudoRS ToolKind = "sudo-rs"
	ToolDoas   ToolKind = "doas"
	ToolRun0   ToolKind = "run0"
)

// Tool describes the privilege tool found on this machine.
type Tool struct {
	Kind ToolKind
	Path string
	// Version holds the first line of `sudo --version`, when the tool answers
	// to the name sudo; otherwise it stays empty.
	Version string
}

// ErrNoPrivilegeTool reports a PATH with none of sudo, sudo-rs, doas or run0.
var ErrNoPrivilegeTool = errors.New("no privilege tool found: install sudo, sudo-rs, doas or run0")

// DetectTool finds the privilege tool to use, preferring sudo, then sudo-rs,
// doas and run0. lookPath resolves a name on PATH (exec.LookPath in
// production). Some distributions ship sudo-rs under the name sudo, so a
// found sudo gets asked for its version.
func DetectTool(ctx context.Context, r Runner, lookPath func(string) (string, error)) (Tool, error) {
	if p, err := lookPath("sudo"); err == nil {
		res, err := r.Run(ctx, Cmd{Name: p, Args: []string{"--version"}})
		if err != nil {
			return Tool{}, fmt.Errorf("asking sudo for its version: %w", err)
		}
		first, _, _ := strings.Cut(string(res.Stdout), "\n")
		kind := ToolSudo
		if strings.HasPrefix(first, "sudo-rs") {
			kind = ToolSudoRS
		}
		return Tool{Kind: kind, Path: p, Version: strings.TrimSpace(first)}, nil
	}
	for _, kind := range []ToolKind{ToolSudoRS, ToolDoas, ToolRun0} {
		if p, err := lookPath(string(kind)); err == nil {
			return Tool{Kind: kind, Path: p}, nil
		}
	}
	return Tool{}, ErrNoPrivilegeTool
}

// PrivRunner runs Root commands through the privilege tool with -n, so they
// never prompt while the interface owns the terminal. Other commands go to
// Inner unchanged.
//
// The privilege tool resets the environment, so a Root command may not carry
// Env: PrivRunner refuses it rather than drop it without a word. The command
// line stays exactly `<tool> -n <name> <args…>`, which lets a sudoers rule
// such as the cc-launch drop-in match it.
type PrivRunner struct {
	Inner Runner
	Tool  Tool
}

// Run implements Runner.
func (p PrivRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if !c.Root {
		return p.Inner.Run(ctx, c)
	}
	if len(c.Env) > 0 {
		return Result{}, fmt.Errorf("%s: %s would drop Env from a root command", c.Name, p.Tool.Kind)
	}
	args := append([]string{"-n", c.Name}, c.Args...)
	return p.Inner.Run(ctx, Cmd{Name: p.Tool.Path, Args: args, Stdin: c.Stdin})
}

// WriteRootFile writes data to a root-owned file at path with the given mode.
// It creates the file empty with its final mode first, then fills it through
// tee from the in-memory bytes, so the content never sits in a file with
// looser permissions. r must handle Root commands (a PrivRunner).
func WriteRootFile(ctx context.Context, r Runner, path string, data []byte, mode os.FileMode) error {
	steps := []Cmd{
		{Name: "install", Args: []string{"-m", fmt.Sprintf("%04o", mode.Perm()), "-T", "/dev/null", path}, Root: true},
		{Name: "tee", Args: []string{"--", path}, Stdin: data, Root: true},
	}
	for _, c := range steps {
		if err := runChecked(ctx, r, c); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

// runChecked runs c and turns a non-zero exit into an error carrying stderr.
func runChecked(ctx context.Context, r Runner, c Cmd) error {
	res, err := r.Run(ctx, c)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s exited with status %d: %s", c.Name, res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
