// Package fetch downloads and verifies everything setup pulls from the
// network: the CodeCheck plugin, the Firecracker release, the kernel, the
// Ubuntu image and Claude Code. Nothing it fetches ever runs on the host.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

var (
	ErrNotHTTPS       = errors.New("only https URLs allowed")
	ErrCommitMismatch = errors.New("fetched commit differs from the pin")
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// pluginPaths names what the guest build takes from CodeCheck.
var pluginPaths = []string{".claude-plugin", "skills", "LICENSE.txt"}

// CodeCheck fetches one pinned CodeCheck commit into a bare cache and
// archives the plugin files from it.
type CodeCheck struct {
	Runner   system.Runner
	FS       system.FS // nil means the real filesystem
	CacheDir string    // ~/.cache/codecheck-sandbox/codecheck.git
	RepoURL  string
	Commit   string

	allowFileURLs bool // tests only: fetch from a local file:// repository
}

// Archive fetches the pinned commit (a shallow, HTTPS-only fetch with hooks
// off and the user's git config ignored), checks that FETCH_HEAD matches the
// pin, and returns a tar of the plugin files.
func (c CodeCheck) Archive(ctx context.Context) ([]byte, error) {
	if !commitPattern.MatchString(c.Commit) {
		return nil, fmt.Errorf("CodeCheck commit %q: want 40 hex characters", c.Commit)
	}
	if err := c.checkURL(); err != nil {
		return nil, err
	}
	fsys := c.FS
	if fsys == nil {
		fsys = system.OSFS{}
	}
	if _, err := fsys.Stat(filepath.Join(c.CacheDir, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		if _, err := c.git(ctx, "init", "--bare", "-q", c.CacheDir); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err := c.git(ctx, "-C", c.CacheDir, "fetch", "--depth", "1", c.RepoURL, c.Commit); err != nil {
		return nil, err
	}
	head, err := c.git(ctx, "-C", c.CacheDir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	if got := strings.TrimSpace(string(head)); got != c.Commit {
		return nil, fmt.Errorf("%w: got %s, want %s", ErrCommitMismatch, got, c.Commit)
	}
	args := append([]string{"-C", c.CacheDir, "archive", "--format=tar", c.Commit, "--"}, pluginPaths...)
	return c.git(ctx, args...)
}

func (c CodeCheck) checkURL() error {
	u, err := url.Parse(c.RepoURL)
	if err != nil {
		return fmt.Errorf("%q: %w", c.RepoURL, ErrNotHTTPS)
	}
	if u.Scheme == "https" && u.Host != "" {
		return nil
	}
	if c.allowFileURLs && u.Scheme == "file" {
		return nil
	}
	return fmt.Errorf("%q: %w", c.RepoURL, ErrNotHTTPS)
}

// git runs git with hooks off, every transport but HTTPS refused, and no
// system or global config, so a url.insteadOf in ~/.gitconfig cannot
// redirect the fetch.
func (c CodeCheck) git(ctx context.Context, args ...string) ([]byte, error) {
	fileAllow := "never"
	if c.allowFileURLs {
		fileAllow = "always"
	}
	hardened := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.file.allow=" + fileAllow,
	}
	res, err := c.Runner.Run(ctx, system.Cmd{
		Name: "git",
		Args: append(hardened, args...),
		Env:  []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"},
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("git %s: exit %d: %s", args[len(args)-1], res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return res.Stdout, nil
}
