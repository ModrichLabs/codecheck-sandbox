package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// gitT runs git for test setup with a clean environment and fixed identity.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	res, err := system.ExecRunner{}.Run(context.Background(), system.Cmd{
		Name: "git", Args: append([]string{"-C", dir}, args...), CleanEnv: true,
		Env: []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com"},
	})
	if err != nil || res.Code != 0 {
		t.Fatalf("git %v: %v %s", args, err, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// repo makes a repository with one commit on main.
func repo(t *testing.T, objectFormat string) string {
	t.Helper()
	dir := t.TempDir()
	args := []string{"init", "-q", "-b", "main"}
	if objectFormat != "" {
		args = append(args, "--object-format="+objectFormat)
	}
	gitT(t, dir, args...)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o644)
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func TestHardenStripsConfigAndHooks(t *testing.T) {
	dir := repo(t, "")
	gitT(t, dir, "config", "diff.external", "/tmp/evil")
	gitT(t, dir, "config", "core.fsmonitor", "/tmp/evil")
	gitT(t, dir, "config", "core.pager", "/tmp/evil")
	gitT(t, dir, "remote", "add", "origin", "https://user:ghp_secrettoken@github.com/org/repo")
	os.WriteFile(filepath.Join(dir, ".git/hooks/post-checkout"), []byte("#!/bin/sh\nevil\n"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git/config.worktree"), []byte("[core]\n\tfsmonitor = /tmp/evil\n"), 0o644)

	if err := Harden(context.Background(), system.ExecRunner{}, dir); err != nil {
		t.Fatalf("Harden: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".git/config"))
	for _, bad := range []string{"external", "fsmonitor", "pager", "remote", "ghp_secrettoken"} {
		if strings.Contains(string(cfg), bad) {
			t.Errorf(".git/config still holds %q:\n%s", bad, cfg)
		}
	}
	for _, gone := range []string{".git/hooks", ".git/config.worktree"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s stayed: %v", gone, err)
		}
	}
	// The repository still works.
	if got := gitT(t, dir, "log", "--format=%s"); got != "first" {
		t.Errorf("git log after Harden = %q", got)
	}
}

func TestHardenKeepsASHA256Repository(t *testing.T) {
	dir := repo(t, "sha256")
	if err := Harden(context.Background(), system.ExecRunner{}, dir); err != nil {
		t.Fatalf("Harden: %v", err)
	}
	if got := gitT(t, dir, "rev-parse", "--show-object-format"); got != "sha256" {
		t.Errorf("object format = %q", got)
	}
	if got := gitT(t, dir, "log", "--format=%s"); got != "first" {
		t.Errorf("git log after Harden = %q", got)
	}
}
