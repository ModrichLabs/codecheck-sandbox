package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Env holds what the commands take from the process, so tests can give them
// a fake home, environment and terminal.
type Env struct {
	FS      system.FS
	Environ map[string]string
	Home    string
	// TTY says stdin and stdout both reach a terminal.
	TTY bool
	In  io.Reader
	// Sandbox opens the sandbox for review and selftest; nil means the
	// real one (newSandbox). Tests pass a fake.
	Sandbox func(Env, Options) (Sandbox, error)
	// Setup opens the host for setup; nil means the real one
	// (newSetupHost). Tests pass fake steps, manifest and lock.
	Setup func(Env, Options) (*SetupHost, error)
	// EUID holds the effective user id; setup refuses root.
	EUID int
}

// OpenSetup returns the host to set up.
func (e Env) OpenSetup(o Options) (*SetupHost, error) {
	if e.Setup != nil {
		return e.Setup(e, o)
	}
	return newSetupHost(e, o)
}

// Sandbox runs reviews and selftests (review.Pipeline, behind cc-launch).
type Sandbox interface {
	Review(ctx context.Context, o review.Options, status func(review.Status), console io.Writer) (review.RunResult, error)
	Selftest(ctx context.Context, ports []int, status func(review.Status), console io.Writer) (review.RunResult, error)
}

// OpenSandbox returns the sandbox to run against.
func (e Env) OpenSandbox(o Options) (Sandbox, error) {
	if e.Sandbox != nil {
		return e.Sandbox(e, o)
	}
	return newSandbox(e, o)
}

// ProcessEnv reads the real process.
func ProcessEnv() Env {
	environ := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			environ[k] = v
		}
	}
	home, _ := os.UserHomeDir()
	if environ["HOME"] == "" {
		environ["HOME"] = home
	}
	return Env{
		FS:      system.OSFS{},
		Environ: environ,
		Home:    home,
		TTY:     term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()),
		In:      os.Stdin,
		EUID:    os.Geteuid(),
	}
}

// ConfigPath names config.toml.
func (e Env) ConfigPath() string { return config.Path(e.Environ) }

// StateDir names ~/.local/state/codecheck-sandbox.
func (e Env) StateDir() string { return config.StateDir(e.Environ) }

// TelemetryDir names the telemetry state folder.
func (e Env) TelemetryDir() string { return filepath.Join(e.StateDir(), "telemetry") }

// LogDir names the log folder.
func (e Env) LogDir() string { return filepath.Join(e.StateDir(), "log") }

// LoadConfig reads config.toml; found stays false when none exists yet.
func (e Env) LoadConfig() (c config.Config, found bool, err error) {
	return config.Load(e.FS, e.ConfigPath(), e.Home)
}

// SaveConfig writes config.toml.
func (e Env) SaveConfig(c config.Config) error {
	return config.Save(e.FS, e.ConfigPath(), e.Home, c)
}
