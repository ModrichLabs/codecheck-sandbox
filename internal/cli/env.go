package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
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
