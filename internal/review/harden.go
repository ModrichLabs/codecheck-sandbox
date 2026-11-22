package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Harden leaves a clone that git can read but that runs nothing: .git/config
// shrinks to the core settings git needs (keeping a SHA-256 repository's
// object format), with no remote, since its URL can carry a token and the VM
// never fetches; hooks and any worktree config go.
func Harden(ctx context.Context, r system.Runner, clone string) error {
	format, err := git(ctx, r, false, "-C", clone, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	cfg := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = false\n"
	if strings.TrimSpace(string(format)) == "sha256" {
		cfg = "[core]\n\trepositoryformatversion = 1\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = false\n" +
			"[extensions]\n\tobjectformat = sha256\n"
	}
	gitDir := filepath.Join(clone, ".git")
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
		return err
	}
	for _, p := range []string{"hooks", "config.worktree"} {
		if err := os.RemoveAll(filepath.Join(gitDir, p)); err != nil {
			return err
		}
	}
	return nil
}
