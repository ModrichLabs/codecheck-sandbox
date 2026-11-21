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

// The privilege tools the engine knows. Each has a flag that fails at once
// instead of prompting for a password: -n, or --no-ask-password for run0.
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

// PrivRunner runs Root commands through the privilege tool without letting it
// prompt, so nothing draws over the interface. Other commands go to Inner
// unchanged.
//
// The command line reads `<tool> -n -- <name> <args…>`: the `--` keeps the
// command's own flags away from the tool's option parser, and the command
// itself stays unwrapped, so a sudoers rule such as the cc-launch drop-in
// still matches it. run0 instead gets `--no-ask-password` (which older run0
// releases know, unlike -n) and `--setenv=LC_ALL=C`, since it starts the
// command in a fresh environment. sudo's env_keep usually keeps LC_ALL; doas
// drops it, so parsed root output should stick to JSON.
//
// The privilege tool resets the environment, so a Root command may not carry
// Env: PrivRunner refuses it rather than drop it without a word.
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
	if c.Stdout != nil || c.Stderr != nil || c.StdinFile != nil || c.Dir != "" || c.Chroot != "" {
		return Result{}, fmt.Errorf("%s: a root command can't stream output, change folder or chroot", c.Name)
	}
	args := []string{"-n"}
	if p.Tool.Kind == ToolRun0 {
		args = []string{"--no-ask-password", "--setenv=LC_ALL=C"}
	}
	args = append(append(args, "--", c.Name), c.Args...)
	res, err := p.Inner.Run(ctx, Cmd{Name: p.Tool.Path, Args: args, Stdin: c.Stdin})
	if err == nil && res.Code != 0 && needsLogin(res.Stderr) {
		return res, fmt.Errorf("%s: %w: %s", c.Name, ErrLoginExpired, strings.TrimSpace(string(res.Stderr)))
	}
	return res, err
}

// loginReplies pair the prefix a privilege tool puts on its own messages with
// what it says when -n meets a missing login: sudo and run0 checked on a real
// host, sudo-rs and doas from their sources. Matching the prefix too keeps a
// root command's own "authorization required" from passing for one.
var loginReplies = []struct{ prefix, says string }{
	{"sudo:", "a password is required"},
	{"sudo-rs:", "authentication is required"},
	{"sudo:", "authentication is required"}, // sudo-rs installed as sudo
	{"doas:", "authentication required"},
	{"doas:", "authorization required"},
	{"failed to start transient service unit:", "requires interactive authentication"}, // run0
}

// needsLogin reports whether a line of stderr comes from the privilege tool
// and says it wanted a password, which the engine treats like an expired
// login and answers with a handover.
func needsLogin(stderr []byte) bool {
	for line := range strings.Lines(strings.ToLower(string(stderr))) {
		line = strings.TrimSpace(line)
		for _, r := range loginReplies {
			if strings.HasPrefix(line, r.prefix) && strings.Contains(line, r.says) {
				return true
			}
		}
	}
	return false
}

// WriteRootFile writes data to a root-owned file at path with the given mode.
// It creates the file empty with its final mode first, then fills it through
// dd from the in-memory bytes, so the content never sits in a file with
// looser permissions. dd, unlike tee, prints nothing, so the content never
// comes back as command output either. r must handle Root commands (a
// PrivRunner).
func WriteRootFile(ctx context.Context, r Runner, path string, data []byte, mode os.FileMode) error {
	steps := []Cmd{
		{Name: "install", Args: []string{"-m", fmt.Sprintf("%04o", mode.Perm()), "-T", "/dev/null", path}, Root: true},
		{Name: "dd", Args: []string{"of=" + path, "status=none"}, Stdin: data, Root: true},
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
