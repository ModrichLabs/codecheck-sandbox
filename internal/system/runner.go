// Package system wraps every external command and file the tool touches, so
// tests can swap in fakes and nothing shells out behind the Runner's back.
package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	// Root asks for the command to run as root, through the privilege tool.
	Root bool
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
	cmd.Env = buildEnv(os.Environ(), c.Env)
	cmd.WaitDelay = waitDelay
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	} // A nil Stdin makes os/exec connect /dev/null.
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctxErr := ctx.Err(); ctxErr != nil {
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
