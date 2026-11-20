package logging

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

const key = "sk-ant-api03-PLANTEDKEY"

func readAll(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

func TestSecretNeverReachesTheFile(t *testing.T) {
	dir := t.TempDir()
	r := &system.Redactor{}
	r.Add(key)
	var console bytes.Buffer
	log, closer, err := New(Options{Dir: dir, Redactor: r, Debug: true, Console: &console})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info("saved", "api_key", key)
	log.Debug("request", "header", "x-api-key: "+key)
	log.Error("failed", "err", errors.New("401 for "+key))
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readAll(t, dir); strings.Contains(got, key) || !strings.Contains(got, "[redacted]") {
		t.Errorf("log file leaked or lost the redaction:\n%s", got)
	}
	if strings.Contains(console.String(), key) {
		t.Errorf("--debug console leaked the key:\n%s", console.String())
	}
}

func TestLevelFollowsDebug(t *testing.T) {
	for _, debug := range []bool{false, true} {
		dir := t.TempDir()
		var console bytes.Buffer
		log, closer, err := New(Options{Dir: dir, Redactor: &system.Redactor{}, Debug: debug, Console: &console})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		log.Debug("fine detail")
		log.Info("a step finished")
		_ = closer.Close()

		got := readAll(t, dir)
		if !strings.Contains(got, "a step finished") {
			t.Errorf("debug=%v: info line missing", debug)
		}
		if strings.Contains(got, "fine detail") != debug {
			t.Errorf("debug=%v: debug line present = %v", debug, !debug)
		}
		if (console.Len() > 0) != debug {
			t.Errorf("debug=%v: console output %q", debug, console.String())
		}
	}
}

func TestRotationKeepsFiveFiles(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := New(Options{Dir: dir, Redactor: &system.Redactor{}, MaxSize: 1024})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	line := strings.Repeat("x", 200)
	for i := range 100 {
		log.Info("filler", "i", i, "pad", line)
	}
	_ = closer.Close()

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		info, _ := e.Info()
		if info.Size() > 1024 {
			t.Errorf("%s holds %d bytes, over the 1024 cap", e.Name(), info.Size())
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", e.Name(), info.Mode().Perm())
		}
	}
	want := []string{"codecheck-sandbox.log", "codecheck-sandbox.log.1", "codecheck-sandbox.log.2", "codecheck-sandbox.log.3", "codecheck-sandbox.log.4"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("files = %v, want %v", names, want)
	}
	// The newest line sits in the current file.
	cur, _ := os.ReadFile(filepath.Join(dir, "codecheck-sandbox.log"))
	if !strings.Contains(string(cur), `"i":99`) {
		t.Errorf("current file lacks the last line:\n%s", cur)
	}
}

func TestNewCreatesAPrivateFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "log")
	_, closer, err := New(Options{Dir: dir, Redactor: &system.Redactor{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = closer.Close()
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("log folder = %v, %v; want mode 0700", info, err)
	}
}
