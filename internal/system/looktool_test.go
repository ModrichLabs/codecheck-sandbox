package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookToolSearchesTheSbinFolders(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cc-test-tool"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "cc-not-runnable"), []byte("x"), 0o644)
	defer func(d []string) { sbinDirs = d }(sbinDirs)
	sbinDirs = []string{dir}
	t.Setenv("PATH", t.TempDir())

	if p, err := LookTool("cc-test-tool"); err != nil || p != filepath.Join(dir, "cc-test-tool") {
		t.Errorf("LookTool = %q, %v", p, err)
	}
	if _, err := LookTool("cc-not-runnable"); err == nil {
		t.Error("LookTool returned a file that can't run")
	}
	if _, err := LookTool("cc-missing"); err == nil {
		t.Error("LookTool found a missing tool")
	}
}
