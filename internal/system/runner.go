// Package system wraps every external command and file the tool touches, so
// tests can swap in fakes and nothing shells out behind the Runner's back.
package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Cmd describes one external command. Name and Args go straight to execve: no
// shell ever parses them.
type Cmd struct {
	Name  string
	Args  []string
	Stdin []byte
	// Env adds to (or overrides) the inherited environment. LC_ALL always ends
	// up as C, whatever Env says.
	Env []string
	// CleanEnv starts from a minimal environment instead of inheriting the
	// caller's: only the variables in cleanEnvKeep survive, plus Env. Use it
	// where an inherited variable could weaken a check (GIT_SSL_NO_VERIFY,
	// GIT_CONFIG_COUNT and the like).
	CleanEnv bool
	// Root asks for the command to run as root, through the privilege tool.
	Root bool
	// Stdout and Stderr, when set, receive the command's output as it runs,
	// and Result then holds none of that stream. Use them for output too
	// large to keep in memory.
	Stdout, Stderr io.Writer
	// Dir sets the working folder; empty keeps the caller's.
	Dir string
	// Chroot runs the command with this folder as its root. Name and Dir then
	// name paths inside it. It needs CAP_SYS_CHROOT, so only the guest's init
	// uses it.
	Chroot string
}

// cleanEnvKeep lists what a CleanEnv command still inherits: the search path,
// the home folder, and the proxy and CA settings a corporate network needs
// (TLS still guards what passes through them).
var cleanEnvKeep = []string{
	"PATH", "HOME",
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

// Result holds what a finished command produced. Code carries the exit status;
// a non-zero Code does not count as an error.
type Result struct {
	Stdout, Stderr []byte
	Code           int
}

// Runner runs external commands. Run returns an error only when the command
// could not start or did not finish (for example, the context ended).
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// ErrRootNeeded reports a Root command handed to a runner without privilege.
var ErrRootNeeded = errors.New("command needs root: run it through the privilege runner")

// ExecRunner runs commands directly with os/exec, as the current user.
type ExecRunner struct{}

// waitDelay bounds how long Run waits for output pipes after the context ends.
const waitDelay = time.Second

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Root {
		return Result{}, fmt.Errorf("%s: %w", c.Name, ErrRootNeeded)
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	base := os.Environ()
	if c.CleanEnv {
		base = keepOnly(base, cleanEnvKeep)
	}
	cmd.Env = buildEnv(base, c.Env)
	cmd.WaitDelay = waitDelay
	cmd.Dir = c.Dir
	if c.Chroot != "" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Chroot: c.Chroot}
	}
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	} // A nil Stdin makes os/exec connect /dev/null.
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	}
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	}

	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	// Blame the context only for a run that failed: a command that exited
	// cleanly just before the deadline still succeeded.
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		return res, fmt.Errorf("%s: %w", c.Name, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Code = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("%s: %w", c.Name, err)
	}
	return res, nil
}

// keepOnly returns the entries of env whose names appear in names.
func keepOnly(env, names []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		for _, n := range names {
			if name == n {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

// buildEnv merges extra into base, drops every LC_ALL entry and appends
// LC_ALL=C, so parsed output never depends on the user's locale.
func buildEnv(base, extra []string) []string {
	env := make([]string, 0, len(base)+len(extra)+1)
	for _, kv := range append(append([]string{}, base...), extra...) {
		if strings.HasPrefix(kv, "LC_ALL=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "LC_ALL=C")
}
