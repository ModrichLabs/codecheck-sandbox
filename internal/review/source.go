package review

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Source names what to review.
type Source struct {
	Target string // an https:// URL or a local folder
	PR     int    // a pull request number, or 0
	Base   string // a ref to diff against, or ""
}

// Prepared describes a ready work folder.
type Prepared struct {
	Name   string
	Dir    string // the work folder, mode 0700
	Clone  string // Dir/code/src: the hardened clone
	Change string // Dir/code/change: diff.patch and status.txt; "" for a full review
	Scope  guestinit.Scope
	Base   string // the baseline commit, for a diff review
	Head   string // the commit under review
}

var (
	refPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@^~-]{0,199}$`)
	nameCleaner = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// Prepare clones the target afresh into a new work folder under workRoot,
// checks out the pull request if one is named, works out the baseline for a
// diff review, hardens the clone, and writes the change files.
func Prepare(ctx context.Context, r system.Runner, workRoot string, s Source, now time.Time) (_ Prepared, err error) {
	local, err := checkTarget(s.Target)
	if err != nil {
		return Prepared{}, err
	}
	switch {
	case s.PR < 0:
		return Prepared{}, fmt.Errorf("pull request %d: want a positive number", s.PR)
	case s.PR > 0 && s.Base != "":
		return Prepared{}, errors.New("--pr and --base: choose one")
	case s.Base != "" && !refPattern.MatchString(s.Base):
		return Prepared{}, fmt.Errorf("base %q: want a branch, tag or commit name", s.Base)
	}

	p := Prepared{Name: nameOf(s.Target), Scope: guestinit.ScopeFull}
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return Prepared{}, err
	}
	p.Dir = filepath.Join(workRoot, p.Name+"-"+now.UTC().Format("20060102-150405"))
	if err := os.Mkdir(p.Dir, 0o700); err != nil {
		return Prepared{}, err
	}
	// A failure leaves nothing behind: the clone may still hold its remote,
	// whose URL can carry a token, until Harden runs.
	dir := p.Dir
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	if err := os.Mkdir(filepath.Join(p.Dir, "code"), 0o755); err != nil {
		return Prepared{}, err
	}
	p.Clone = filepath.Join(p.Dir, "code", "src")
	raw := func(args ...string) ([]byte, error) {
		return git(ctx, r, local, append([]string{"-C", p.Clone}, args...)...)
	}
	g := func(args ...string) (string, error) {
		out, err := raw(args...)
		return strings.TrimSpace(string(out)), err
	}

	if _, err := git(ctx, r, local, "clone", "-q", "--no-local", "--template=", "--no-recurse-submodules", "--", s.Target, p.Clone); err != nil {
		return Prepared{}, err
	}
	switch {
	case s.PR > 0:
		if _, err := g("fetch", "-q", "origin", "refs/pull/"+strconv.Itoa(s.PR)+"/head"); err != nil {
			return Prepared{}, fmt.Errorf("fetching pull request %d: %w", s.PR, err)
		}
		if _, err := g("checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
			return Prepared{}, err
		}
		if p.Base, err = g("merge-base", "HEAD", "origin/HEAD"); err != nil {
			return Prepared{}, fmt.Errorf("finding the pull request's base: %w", err)
		}
	case s.Base != "":
		ref, err := resolveRef(g, s.Base)
		if err != nil {
			return Prepared{}, err
		}
		if p.Base, err = g("merge-base", "HEAD", ref); err != nil {
			return Prepared{}, err
		}
	}
	if p.Head, err = g("rev-parse", "HEAD"); err != nil {
		return Prepared{}, err
	}
	if err := Harden(ctx, r, p.Clone); err != nil {
		return Prepared{}, err
	}
	if p.Base == "" {
		return p, nil
	}

	p.Scope = guestinit.ScopeDiff
	p.Change = filepath.Join(p.Dir, "code", "change")
	if err := os.Mkdir(p.Change, 0o755); err != nil {
		return Prepared{}, err
	}
	// Raw output: trimming would drop a porcelain line's leading space or a
	// patch's last blank context line.
	diff, err := raw("diff", "--no-ext-diff", "--no-textconv", "--no-color", p.Base)
	if err != nil {
		return Prepared{}, err
	}
	status, err := raw("status", "--porcelain")
	if err != nil {
		return Prepared{}, err
	}
	for name, body := range map[string][]byte{"diff.patch": diff, "status.txt": status} {
		if err := os.WriteFile(filepath.Join(p.Change, name), body, 0o644); err != nil {
			return Prepared{}, err
		}
	}
	return p, nil
}

// resolveRef finds a ref as given, then as a branch on origin.
func resolveRef(g func(...string) (string, error), ref string) (string, error) {
	for _, try := range []string{ref, "origin/" + ref} {
		if c, err := g("rev-parse", "--verify", "--quiet", "--end-of-options", try+"^{commit}"); err == nil && c != "" {
			return c, nil
		}
	}
	return "", fmt.Errorf("base %q: no such branch, tag or commit", ref)
}

// checkTarget allows an https URL or an existing folder, and reports which.
func checkTarget(t string) (local bool, err error) {
	if strings.HasPrefix(t, "-") || strings.Contains(t, "::") {
		return false, fmt.Errorf("source %q: want an https URL or a folder", t)
	}
	if u, err := url.Parse(t); err == nil && u.Scheme != "" {
		if u.Scheme != "https" || u.Host == "" {
			return false, fmt.Errorf("source %q: only https URLs", t)
		}
		return false, nil
	}
	if strings.Contains(t, ":") && !strings.HasPrefix(t, "/") && !strings.HasPrefix(t, ".") {
		return false, fmt.Errorf("source %q: only https URLs", t) // user@host:path
	}
	fi, err := os.Stat(t)
	if err != nil || !fi.IsDir() {
		return false, fmt.Errorf("source %q: no such folder", t)
	}
	return true, nil
}

// nameOf makes a short folder name from the target.
func nameOf(t string) string {
	if u, err := url.Parse(t); err == nil && u.Scheme != "" {
		t = u.Path
	}
	base := strings.ToLower(path.Base(strings.TrimRight(t, "/")))
	base = strings.TrimSuffix(base, ".git")
	base = strings.Trim(nameCleaner.ReplaceAllString(base, "-"), "-.")
	if base == "" {
		return "review"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return base
}
