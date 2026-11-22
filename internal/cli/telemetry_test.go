package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"

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

// testTree builds root → parent → child, where child sets ran.
func testTree(ran *bool) *cobra.Command {
	root := &cobra.Command{Use: "root", SilenceUsage: true, SilenceErrors: true}
	parent := &cobra.Command{Use: "telemetry"}
	parent.AddCommand(&cobra.Command{Use: "off", RunE: func(*cobra.Command, []string) error {
		*ran = true
		return nil
	}})
	root.AddCommand(parent)
	return root
}

func TestTheClientComesAfterTheCommandRuns(t *testing.T) {
	// `telemetry off` must change the setting before the wrapper asks for a
	// client, so the wrapper sees the new consent and sends nothing.
	var ran, builtAfter bool
	root := testTree(&ran)
	fake := &fakeClient{}
	withTelemetry(root, testEnv(), &Options{}, func(Options) telemetry.Client {
		builtAfter = ran
		return fake
	})
	root.SetArgs([]string{"telemetry", "off"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !builtAfter {
		t.Error("the wrapper built its client before the command ran")
	}
}

func TestNestedSubcommandsGetWrapped(t *testing.T) {
	var ran bool
	root := testTree(&ran)
	fake := &fakeClient{}
	withTelemetry(root, testEnv(), &Options{}, func(Options) telemetry.Client { return fake })
	root.SetArgs([]string{"telemetry", "off"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.events) != 1 || fake.flushes != 1 {
		t.Fatalf("events = %+v, flushes = %d; want the nested command recorded", fake.events, fake.flushes)
	}
	if fake.events[0].(telemetry.CommandRun).Command != "telemetry" {
		t.Errorf("command = %v, want the top-level name telemetry", fake.events[0])
	}
}
