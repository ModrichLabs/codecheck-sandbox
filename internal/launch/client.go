package launch

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// cc-launch's exit codes, which cmd/cc-launch documents and returns.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitBusy        = 3
	ExitRefused     = 4
	ExitBoot        = 5
	ExitTimeout     = 6
	ExitResults     = 7
	ExitEnvironment = 8
	ExitInterrupted = 130
)

var exitMeanings = map[int]string{
	ExitFailure:     "cc-launch failed; its output above says why",
	ExitUsage:       "cc-launch refused its arguments (a bug in codecheck-sandbox)",
	ExitBusy:        "another sandbox run holds the lock; wait for it to finish",
	ExitRefused:     "a pre-boot check refused the run; run codecheck-sandbox doctor",
	ExitBoot:        "the VM failed to start",
	ExitTimeout:     "the VM ran past its time limit and got stopped",
	ExitResults:     "the results could not be copied off the output disk",
	ExitEnvironment: "the sandbox lacks its setup; run codecheck-sandbox setup",
	ExitInterrupted: "the run got interrupted",
}

// ExitError reports a cc-launch run that ended with a non-zero exit code.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	if m, ok := exitMeanings[e.Code]; ok {
		return fmt.Sprintf("cc-launch exit %d: %s", e.Code, m)
	}
	return fmt.Sprintf("cc-launch exit %d", e.Code)
}

// Streamer runs a root command with its stderr streamed (system.PrivRunner).
type Streamer interface {
	RunStreaming(ctx context.Context, c system.Cmd, console io.Writer) (system.Result, error)
}

// Client runs cc-launch for the CLI, through the privilege tool and the
// sudoers rule, with the VM's serial console streamed to Console.
type Client struct {
	Priv    Streamer
	Console io.Writer
	Path    string // empty means the installed cc-launch
}

// Run boots the review or selftest the run's config names.
func (c Client) Run(ctx context.Context, id RunID) error { return c.launch(ctx, id.String()) }

// Build boots the guest image build.
func (c Client) Build(ctx context.Context, id RunID) error {
	return c.launch(ctx, "build", id.String())
}

// Clean removes the run folder.
func (c Client) Clean(ctx context.Context, id RunID) error {
	return c.launch(ctx, "clean", id.String())
}

func (c Client) launch(ctx context.Context, args ...string) error {
	path := c.Path
	if path == "" {
		path = filepath.Join(DefaultPaths().BinDir, "cc-launch")
	}
	res, err := c.Priv.RunStreaming(ctx, system.Cmd{Name: path, Args: args}, c.Console)
	if err != nil {
		return err
	}
	if res.Code != ExitOK {
		return &ExitError{Code: res.Code}
	}
	return nil
}
