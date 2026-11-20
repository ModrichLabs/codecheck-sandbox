package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

type fakeClient struct {
	events  []telemetry.Event
	flushes int
}

func (f *fakeClient) Capture(e telemetry.Event) { f.events = append(f.events, e) }
func (f *fakeClient) Flush(context.Context)     { f.flushes++ }

func runWith(t *testing.T, args ...string) (*fakeClient, Options, error) {
	t.Helper()
	fake := &fakeClient{}
	var seen Options
	cmd := NewRootCommandWith(func(o Options) telemetry.Client {
		seen = o
		return fake
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return fake, seen, err
}

func TestEveryCommandRecordsCommandRunAndFlushes(t *testing.T) {
	fake, _, err := runWith(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if len(fake.events) != 1 || fake.flushes != 1 {
		t.Fatalf("events = %+v, flushes = %d; want one command_run and one flush", fake.events, fake.flushes)
	}
	run := fake.events[0].(telemetry.CommandRun)
	if run.Command != "version" || run.Outcome != telemetry.OutcomeOK || run.CLIVersion != telemetry.Version(buildinfo.Version) {
		t.Errorf("command_run = %+v", run)
	}
}

func TestAFailingCommandAlsoSendsAnException(t *testing.T) {
	fake, _, err := runWith(t, "doctor")
	if err == nil {
		t.Fatal("doctor: want the not-implemented error")
	}
	if len(fake.events) != 2 || fake.flushes != 1 {
		t.Fatalf("events = %+v, flushes = %d", fake.events, fake.flushes)
	}
	if fake.events[0].(telemetry.CommandRun).Outcome != telemetry.OutcomeFailed {
		t.Errorf("command_run = %+v, want failed", fake.events[0])
	}
	if _, ok := fake.events[1].(telemetry.Exception); !ok {
		t.Errorf("second event = %T, want an Exception", fake.events[1])
	}
}

func TestTheClientFactorySeesTheGlobalFlags(t *testing.T) {
	_, seen, _ := runWith(t, "--no-telemetry", "--yes", "version")
	if !seen.NoTelemetry || !seen.Yes {
		t.Errorf("factory got %+v, want NoTelemetry and Yes set", seen)
	}
}

func TestDefaultTelemetryStaysOffWithoutStoredConsent(t *testing.T) {
	// config.toml arrives in M13; until then nothing can switch telemetry on.
	if _, ok := defaultTelemetry(Options{}).(telemetry.Noop); !ok {
		t.Error("defaultTelemetry must return Noop while no consent can exist")
	}
}
