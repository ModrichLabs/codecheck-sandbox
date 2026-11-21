package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

var now = time.Date(2026, 11, 21, 10, 15, 0, 0, time.UTC)

func commitFile(t *testing.T, dir, name, body, msg string) string {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", msg)
	return gitT(t, dir, "rev-parse", "HEAD")
}

func TestPrepareFullReviewOfALocalPath(t *testing.T) {
	src := repo(t, "")
	gitT(t, src, "config", "diff.external", "/tmp/evil") // the source's own config stays behind
	work := t.TempDir()
	p, err := Prepare(context.Background(), system.ExecRunner{}, work, Source{Target: src}, now)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if p.Scope != guestinit.ScopeFull || p.Base != "" || p.Change != "" {
		t.Errorf("prepared %+v, want a full review", p)
	}
	if !strings.HasPrefix(p.Dir, work+"/") || !strings.HasSuffix(p.Dir, "-20261121-101500") {
		t.Errorf("work folder %s", p.Dir)
	}
	if fi, _ := os.Stat(p.Dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("work folder mode %v, want 0700", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(p.Clone, "app.go")); string(b) != "package app\n" {
		t.Errorf("clone lacks app.go")
	}
	cfg, _ := os.ReadFile(filepath.Join(p.Clone, ".git/config"))
	if strings.Contains(string(cfg), "external") || strings.Contains(string(cfg), "remote") {
		t.Errorf("the clone isn't hardened:\n%s", cfg)
	}
}

func TestPrepareDiffAgainstABase(t *testing.T) {
	src := repo(t, "")
	base := gitT(t, src, "rev-parse", "HEAD")
	gitT(t, src, "checkout", "-q", "-b", "feature")
	commitFile(t, src, "new.go", "package app\n\nfunc New() {}\n", "add New")
	gitT(t, src, "checkout", "-q", "main")
	gitT(t, src, "checkout", "-q", "feature") // the clone takes HEAD: feature

	p, err := Prepare(context.Background(), system.ExecRunner{}, t.TempDir(), Source{Target: src, Base: "main"}, now)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if p.Scope != guestinit.ScopeDiff || p.Base != base {
		t.Errorf("scope %s base %s, want diff against %s", p.Scope, p.Base, base)
	}
	diff, _ := os.ReadFile(filepath.Join(p.Change, "diff.patch"))
	if !strings.Contains(string(diff), "+func New() {}") {
		t.Errorf("diff.patch =\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(p.Change, "status.txt")); err != nil {
		t.Errorf("status.txt: %v", err)
	}
	if filepath.Dir(p.Change) != p.Dir || filepath.Base(p.Change) != "change" {
		t.Errorf("change folder %s, want beside the clone", p.Change)
	}
}

func TestPreparePullRequest(t *testing.T) {
	// A bare origin with a pull request ref, as GitHub publishes one.
	src := repo(t, "")
	base := gitT(t, src, "rev-parse", "HEAD")
	gitT(t, src, "checkout", "-q", "-b", "pr")
	head := commitFile(t, src, "fix.go", "package app\n", "fix")
	gitT(t, src, "checkout", "-q", "main")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitT(t, t.TempDir(), "clone", "-q", "--bare", src, origin)
	gitT(t, origin, "update-ref", "refs/pull/7/head", head)
	gitT(t, origin, "branch", "-D", "pr")

	p, err := Prepare(context.Background(), system.ExecRunner{}, t.TempDir(), Source{Target: origin, PR: 7}, now)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if p.Head != head || p.Base != base || p.Scope != guestinit.ScopeDiff {
		t.Errorf("head %s base %s scope %s; want head %s, base %s, diff", p.Head, p.Base, p.Scope, head, base)
	}
	if _, err := os.Stat(filepath.Join(p.Clone, "fix.go")); err != nil {
		t.Error("the pull request isn't checked out")
	}
}

func TestPrepareRefusesBadSources(t *testing.T) {
	src := repo(t, "")
	for name, s := range map[string]Source{
		"plain http":       {Target: "http://example.com/org/repo"},
		"ssh":              {Target: "ssh://git@example.com/org/repo"},
		"scp-like":         {Target: "git@example.com:org/repo"},
		"option":           {Target: "--upload-pack=touch /tmp/x"},
		"ext transport":    {Target: "ext::sh -c touch% /tmp/x"},
		"missing path":     {Target: filepath.Join(t.TempDir(), "nope")},
		"option as base":   {Target: src, Base: "--output=/tmp/x"},
		"unknown base":     {Target: src, Base: "no-such-branch"},
		"both pr and base": {Target: src, Base: "main", PR: 1},
		"negative pr":      {Target: src, PR: -1},
	} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			if _, err := Prepare(context.Background(), system.ExecRunner{}, work, s, now); err == nil {
				t.Error("Prepare accepted it")
			}
		})
	}
}

func TestNameFromTarget(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/org/My-Repo.git": "my-repo",
		"https://github.com/org/repo/":       "repo",
		"/home/u/src/Project X":              "project-x",
		"/":                                  "review",
	} {
		if got := nameOf(in); got != want {
			t.Errorf("nameOf(%q) = %q, want %q", in, got, want)
		}
	}
}
