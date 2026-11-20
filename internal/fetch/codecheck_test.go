package fetch

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// upstream builds a bare repository holding one commit with the plugin
// layout, and returns its file:// URL and the commit.
func upstream(t *testing.T) (string, string) {
	t.Helper()
	run := func(dir string, args ...string) string {
		t.Helper()
		res, err := system.ExecRunner{}.Run(context.Background(), system.Cmd{
			Name: "git", Args: append([]string{"-C", dir}, args...),
			Env: []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid"},
		})
		if err != nil || res.Code != 0 {
			t.Fatalf("git %v: %v %s", args, err, res.Stderr)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	work := t.TempDir()
	for path, body := range map[string]string{
		".claude-plugin/plugin.json": `{"name":"codecheck"}`,
		"skills/review/SKILL.md":     "# review",
		"LICENSE.txt":                "licence",
		"README.md":                  "not part of the build inputs",
		"scripts/evil.sh":            "echo nope",
	} {
		p := filepath.Join(work, path)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(work, "init", "-q", "-b", "main")
	run(work, "add", ".")
	run(work, "commit", "-q", "-m", "init")
	commit := run(work, "rev-parse", "HEAD")
	bare := filepath.Join(t.TempDir(), "up.git")
	run(work, "clone", "-q", "--bare", work, bare)
	return "file://" + bare, commit
}

func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(data))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name)
		}
	}
	slices.Sort(names)
	return names
}

func TestCodeCheckFetchesThePinnedCommit(t *testing.T) {
	url, commit := upstream(t)
	cc := CodeCheck{Runner: system.ExecRunner{}, CacheDir: filepath.Join(t.TempDir(), "codecheck.git"), RepoURL: url, Commit: commit, allowFileURLs: true}
	data, err := cc.Archive(context.Background())
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	want := []string{".claude-plugin/plugin.json", "LICENSE.txt", "skills/review/SKILL.md"}
	if got := tarNames(t, data); !slices.Equal(got, want) {
		t.Errorf("archive holds %v, want only %v", got, want)
	}
	// A second run reuses the cache.
	if _, err := cc.Archive(context.Background()); err != nil {
		t.Errorf("second Archive: %v", err)
	}
}

func TestCodeCheckRefusesAMismatchedCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	f := &system.FakeRunner{}
	f.On("git").Return(system.Result{})
	f.On("git").Return(system.Result{}) // fetch
	f.On("git").Return(system.Result{Stdout: []byte(strings.Repeat("b", 40) + "\n")})
	cc := CodeCheck{Runner: f, CacheDir: "/cache/codecheck.git", RepoURL: "https://example.invalid/CodeCheck", Commit: commit}
	_, err := cc.Archive(context.Background())
	if !errors.Is(err, ErrCommitMismatch) {
		t.Errorf("err = %v, want ErrCommitMismatch", err)
	}
}

func TestCodeCheckRefusesNonHTTPS(t *testing.T) {
	for _, url := range []string{"http://github.com/x/y", "git://github.com/x/y", "ssh://git@github.com/x/y", "file:///tmp/x", "-uploadpack=evil", "ext::sh -c evil"} {
		cc := CodeCheck{Runner: &system.FakeRunner{}, CacheDir: "/c", RepoURL: url, Commit: strings.Repeat("a", 40)}
		if _, err := cc.Archive(context.Background()); !errors.Is(err, ErrNotHTTPS) {
			t.Errorf("%q: err = %v, want ErrNotHTTPS", url, err)
		}
	}
}

func TestCodeCheckRefusesABadCommit(t *testing.T) {
	for _, c := range []string{"", "HEAD", "main", "--upload-pack=evil", strings.Repeat("g", 40), strings.Repeat("a", 39)} {
		cc := CodeCheck{Runner: &system.FakeRunner{}, CacheDir: "/c", RepoURL: "https://github.com/x/y", Commit: c}
		if _, err := cc.Archive(context.Background()); err == nil {
			t.Errorf("commit %q accepted", c)
		}
	}
}

func TestCodeCheckHardensEveryGitCall(t *testing.T) {
	commit := strings.Repeat("c", 40)
	f := &system.FakeRunner{}
	f.On("git").Return(system.Result{})
	f.On("git").Return(system.Result{})
	f.On("git").Return(system.Result{Stdout: []byte(commit + "\n")})
	f.On("git").Return(system.Result{Stdout: []byte("tar")})
	cc := CodeCheck{Runner: f, CacheDir: "/cache/codecheck.git", RepoURL: "https://github.com/x/y", Commit: commit}
	if _, err := cc.Archive(context.Background()); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	for _, c := range f.Calls() {
		argv := strings.Join(c.Args, " ")
		for _, want := range []string{"core.hooksPath=/dev/null", "protocol.allow=never", "protocol.https.allow=always", "protocol.file.allow=never"} {
			if !strings.Contains(argv, want) {
				t.Errorf("git %s lacks -c %s", argv, want)
			}
		}
		if !slices.Contains(c.Env, "GIT_CONFIG_GLOBAL=/dev/null") || !slices.Contains(c.Env, "GIT_CONFIG_NOSYSTEM=1") || !slices.Contains(c.Env, "GIT_TERMINAL_PROMPT=0") {
			t.Errorf("git %s runs with the user's git config: Env = %v", argv, c.Env)
		}
		if c.Root {
			t.Errorf("git %s runs as root", argv)
		}
	}
	fetch := strings.Join(f.Calls()[1].Args, " ")
	if !strings.Contains(fetch, "fetch --depth 1 https://github.com/x/y "+commit) {
		t.Errorf("fetch argv = %s", fetch)
	}
}
