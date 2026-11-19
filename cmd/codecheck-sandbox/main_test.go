package main

import (
	"bytes"
	"testing"
)

func TestVersionFlagPrintsNameAndVersion(t *testing.T) {
	var out bytes.Buffer

	code := run([]string{"--version"}, &out, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got, want := out.String(), "codecheck-sandbox 0.1.0-dev\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
