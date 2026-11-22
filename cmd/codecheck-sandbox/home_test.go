package main

import (
	"os"
	"testing"
)

// TestMain points HOME and the XDG folders at a temporary folder, so a test
// that builds the real environment (ProcessEnv) never writes a log, config
// or cache into the home of whoever runs the tests.
func TestMain(m *testing.M) {
	os.Exit(runWithTempHome(m))
}

func runWithTempHome(m *testing.M) int {
	dir, err := os.MkdirTemp("", "codecheck-cli-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		os.Setenv(k, dir)
	}
	os.Setenv("DO_NOT_TRACK", "1")
	return m.Run()
}
