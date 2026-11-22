package main

import (
	"bytes"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
)

func TestVersionFlagPrintsNameAndVersion(t *testing.T) {
	var out bytes.Buffer

	code := run([]string{"--version"}, &out, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got, want := out.String(), buildinfo.Name+" "+buildinfo.Version+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestErrorsGoToStderrWithExitCodeOne(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// A bad --budget fails before review touches the host.
	code := run([]string{"review", "x", "--budget", "lots", "--no-telemetry"}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got, want := stderr.String(), "codecheck-sandbox: --budget: enter an amount in US dollars, such as 25\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}

func TestUnknownCommandFailsWithExitCodeOne(t *testing.T) {
	var out bytes.Buffer

	if code := run([]string{"frobnicate"}, &out, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
