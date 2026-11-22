package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

// testEnv gives a command a fake home with no terminal.
func testEnv() Env {
	return Env{
		FS:      system.NewMemFS(),
		Environ: map[string]string{"HOME": "/home/u", "USER": "u"},
		Home:    "/home/u",
		EUID:    1000,
	}
}

func runIn(t *testing.T, env Env, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommandEnv(env, func(Options) telemetry.Client { return telemetry.Noop{} })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func telemetryDir(env Env) string { return filepath.Join(config.StateDir(env.Environ), "telemetry") }

func TestTelemetryOnStoresConsentAndAnID(t *testing.T) {
	env := testEnv()
	out, err := runIn(t, env, "telemetry", "on")
	if err != nil {
		t.Fatalf("telemetry on: %v\n%s", err, out)
	}
	c, found, err := config.Load(env.FS, config.Path(env.Environ), env.Home)
	if err != nil || !found || c.Telemetry == nil || !*c.Telemetry {
		t.Errorf("config after on: %+v, found %v, %v", c, found, err)
	}
	if _, err := env.FS.Stat(filepath.Join(telemetryDir(env), telemetry.InstallIDFile)); err != nil {
		t.Errorf("no install id: %v", err)
	}
	if !strings.Contains(out, "on") {
		t.Errorf("output: %q", out)
	}
}

func TestTelemetryOffForgetsEverything(t *testing.T) {
	env := testEnv()
	runIn(t, env, "telemetry", "on")
	env.FS.WriteFile(filepath.Join(telemetryDir(env), telemetry.QueueFile), []byte("{}\n"), 0o600)
	if out, err := runIn(t, env, "telemetry", "off"); err != nil {
		t.Fatalf("telemetry off: %v\n%s", err, out)
	}
	c, _, _ := config.Load(env.FS, config.Path(env.Environ), env.Home)
	if c.Telemetry == nil || *c.Telemetry {
		t.Errorf("config after off: %+v", c)
	}
	for _, f := range []string{telemetry.InstallIDFile, telemetry.QueueFile} {
		if _, err := env.FS.Stat(filepath.Join(telemetryDir(env), f)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived off: %v", f, err)
		}
	}
}

func TestTelemetryStatusSaysWhy(t *testing.T) {
	env := testEnv()
	for _, c := range []struct {
		setup func(Env)
		args  []string
		want  string
	}{
		{nil, nil, "off: not set yet"},
		{func(e Env) { runIn(t, e, "telemetry", "on") }, nil, "on"},
		{nil, []string{"--no-telemetry"}, "off: --no-telemetry"},
		{func(e Env) { e.Environ["DO_NOT_TRACK"] = "1" }, nil, "off: DO_NOT_TRACK"},
		{func(e Env) { delete(e.Environ, "DO_NOT_TRACK"); runIn(t, e, "telemetry", "off") }, nil, "off: turned off"},
	} {
		if c.setup != nil {
			c.setup(env)
		}
		out, err := runIn(t, env, append([]string{"telemetry", "status"}, c.args...)...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("status %v: %q, %v; want %q", c.args, out, err, c.want)
		}
	}
}

func TestTelemetryWithoutASubcommandShowsStatus(t *testing.T) {
	out, err := runIn(t, testEnv(), "telemetry")
	if err != nil || !strings.Contains(out, "off") {
		t.Errorf("telemetry: %q, %v", out, err)
	}
}

// The real factory: consent decides between Noop and PostHog.
func TestRealTelemetryFollowsConsent(t *testing.T) {
	env := testEnv()
	if _, ok := realTelemetry(env)(Options{}).(telemetry.Noop); !ok {
		t.Error("telemetry ran without consent")
	}
	runIn(t, env, "telemetry", "on")
	if _, ok := realTelemetry(env)(Options{}).(telemetry.Noop); ok {
		t.Error("consent given, but telemetry stayed off")
	}
	if _, ok := realTelemetry(env)(Options{NoTelemetry: true}).(telemetry.Noop); !ok {
		t.Error("--no-telemetry didn't win")
	}
	env.Environ["DO_NOT_TRACK"] = "1"
	if _, ok := realTelemetry(env)(Options{}).(telemetry.Noop); !ok {
		t.Error("DO_NOT_TRACK didn't win")
	}
	// A broken config means off, never a failed command.
	env2 := testEnv()
	env2.FS.MkdirAll(filepath.Dir(config.Path(env2.Environ)), 0o700)
	env2.FS.WriteFile(config.Path(env2.Environ), []byte("telemetry = true\nbogus = 1\n"), 0o600)
	if _, ok := realTelemetry(env2)(Options{}).(telemetry.Noop); !ok {
		t.Error("a config that fails to load turned telemetry on")
	}
}
