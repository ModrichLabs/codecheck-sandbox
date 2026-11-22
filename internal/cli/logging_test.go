package cli

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// memLog opens a log in memory, through the redactor as logging.New does.
func memLog(buf *bytes.Buffer) func(bool, *system.Redactor) (*slog.Logger, io.Closer, error) {
	return func(_ bool, red *system.Redactor) (*slog.Logger, io.Closer, error) {
		w := red.Writer(buf)
		return slog.New(slog.NewJSONHandler(w, nil)), w, nil
	}
}

func TestEveryCommandLogsItsRun(t *testing.T) {
	var buf bytes.Buffer
	env := testEnv()
	env.OpenLog = memLog(&buf)
	if _, err := runIn(t, env, "telemetry", "status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"command":"telemetry"`) || !strings.Contains(buf.String(), `"outcome":"ok"`) {
		t.Errorf("log = %s", buf.String())
	}
}

// Setup's key gets registered with the redactor before anything could log
// it, here through a failing step's error.
func TestSetupKeepsTheKeyOutOfTheLog(t *testing.T) {
	var buf bytes.Buffer
	key := "sk-ant-api03-TOPSECRETkeyFORtheLOG"
	f := &fakeSetup{steps: []scriptStep{{id: "host-api-key", phase: step.PhaseHost, fail: errorWith(key)}}}
	env := setupEnv(f, passingSelftest())
	env.Environ["CODECHECK_API_KEY"] = key
	env.OpenLog = memLog(&buf)
	runIn(t, env, "setup", "--yes")
	if !strings.Contains(buf.String(), "host-api-key") {
		t.Errorf("the failure didn't reach the log: %s", buf.String())
	}
	if strings.Contains(buf.String(), "TOPSECRET") {
		t.Errorf("the key reached the log: %s", buf.String())
	}
}

type keyErr string

func (e keyErr) Error() string { return "writing the key " + string(e) + " failed" }

func errorWith(key string) error { return keyErr(key) }
