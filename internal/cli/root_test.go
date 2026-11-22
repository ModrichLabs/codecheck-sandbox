package cli

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
)

// execute runs the root command with args and returns its output and error.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestRootHasEveryLifecycleSubcommand(t *testing.T) {
	want := []string{
		"canary", "doctor", "proxy", "rebuild", "review", "selftest",
		"setup", "telemetry", "uninstall", "version",
	}

	var got []string
	for _, c := range NewRootCommand().Commands() {
		got = append(got, c.Name())
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("subcommands = %v, want %v", got, want)
	}
}

func TestRootDefinesGlobalFlags(t *testing.T) {
	flags := NewRootCommand().PersistentFlags()
	for _, name := range []string{"yes", "dry-run", "plain", "debug", "no-telemetry"} {
		f := flags.Lookup(name)
		if f == nil {
			t.Errorf("missing global flag --%s", name)
			continue
		}
		if f.Value.Type() != "bool" || f.DefValue != "false" {
			t.Errorf("--%s: type %s default %s, want bool default false", name, f.Value.Type(), f.DefValue)
		}
	}
}

func TestStubSubcommandsReportNotImplemented(t *testing.T) {
	for _, name := range []string{
		"setup", "doctor", "rebuild",
		"uninstall",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := execute(t, name)
			if !errors.Is(err, ErrNotImplemented) {
				t.Fatalf("error = %v, want ErrNotImplemented", err)
			}
		})
	}
}

func TestHelpListsCommandsButHidesProxy(t *testing.T) {
	out, err := execute(t, "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, name := range []string{"setup", "doctor", "review", "uninstall", "telemetry"} {
		if !strings.Contains(out, name) {
			t.Errorf("help does not mention %q:\n%s", name, out)
		}
	}
	if strings.Contains(out, "proxy") {
		t.Errorf("help mentions the hidden proxy command:\n%s", out)
	}
	if strings.Contains(out, "completion") {
		t.Errorf("help offers cobra's undocumented completion command:\n%s", out)
	}
	if strings.Contains(out, "-v,") {
		t.Errorf("help offers a -v shorthand for --version:\n%s", out)
	}
}

func TestCompletionCommandDoesNotExist(t *testing.T) {
	if _, err := execute(t, "completion", "bash"); err == nil {
		t.Fatal("completion ran; want an unknown-command error")
	}
}

func TestVersionHasNoShorthand(t *testing.T) {
	if _, err := execute(t, "-v"); err == nil {
		t.Fatal("-v succeeded; want an unknown-shorthand error")
	}
}

func TestVersionSubcommandAndFlagPrintTheSameLine(t *testing.T) {
	want := buildinfo.Name + " " + buildinfo.Version + "\n"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		out, err := execute(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out != want {
			t.Errorf("%v printed %q, want %q", args, out, want)
		}
	}
}
