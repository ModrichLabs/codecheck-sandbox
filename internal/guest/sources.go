package guest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ModrichLabs/codecheck-sandbox/internal/fetch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// sumsMax caps Ubuntu's SHA256SUMS and its signature.
const sumsMax = 1 << 20

// PinnedSources fetches the guest's inputs at their pins, reusing a cached
// file that already matches its pin.
type PinnedSources struct {
	D         fetch.Downloader
	CodeCheck fetch.CodeCheck
	KernelPin pins.KernelPin
	Ubuntu    pins.UbuntuPin
	ClaudePin pins.ClaudeCodePin
	WorkDir   string // holds the npm tarball while it gets read
	// KernelFile, when set, supplies the kernel instead of the pinned URL
	// (setup --kernel), for when the upstream file has moved. It must still
	// match KernelPin.SHA256.
	KernelFile string
}

// ErrKernelUnavailable reports a kernel download that failed, with a hint
// at setup's --kernel fallback.
var ErrKernelUnavailable = errors.New("the guest kernel couldn't download")

// DefaultSources uses the built-in pins, with the CodeCheck cache under
// cacheDir, and the given CodeCheck commit (empty means the pinned one).
func DefaultSources(d fetch.Downloader, r system.Runner, cacheDir, codecheck string) PinnedSources {
	if codecheck == "" {
		codecheck = pins.CodeCheck.Commit
	}
	return PinnedSources{
		D: d,
		CodeCheck: fetch.CodeCheck{Runner: r, CacheDir: filepath.Join(cacheDir, "codecheck.git"),
			RepoURL: pins.CodeCheck.RepoURL, Commit: codecheck},
		KernelPin: pins.Kernel, Ubuntu: pins.Ubuntu, ClaudePin: pins.ClaudeCode, WorkDir: cacheDir,
	}
}

// cached reports whether dest already holds the pinned bytes.
func cached(dest, sha string) bool {
	got, err := fileSHA256(dest)
	return err == nil && got == sha
}

// Kernel implements Sources: a cached copy, then KernelFile, then the
// pinned URL. Whatever the source, only the pinned hash gets accepted.
func (s PinnedSources) Kernel(ctx context.Context, dest string) error {
	if cached(dest, s.KernelPin.SHA256) {
		return nil
	}
	if s.KernelFile != "" {
		return copyPinned(s.KernelFile, dest, s.KernelPin.SHA256)
	}
	if err := s.D.Download(ctx, s.KernelPin.URL, s.KernelPin.SHA256, dest); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("%w from %s: %w. The upstream file may have moved: download vmlinux-%s "+
			"from another source and run setup again with --kernel FILE (it must hash to %s)",
			ErrKernelUnavailable, s.KernelPin.URL, err, s.KernelPin.Version, s.KernelPin.SHA256)
	}
	return nil
}

// copyPinned copies src to dest only when src hashes to sha, through a
// temporary file, so a mismatch never reaches dest.
func copyPinned(src, dest, sha string) error {
	got, err := fileSHA256(src)
	if err != nil {
		return fmt.Errorf("--kernel: %w", err)
	}
	if got != sha {
		return fmt.Errorf("--kernel %s hashes to %s, not the pinned %s; refusing it", src, got, sha)
	}
	tmp := dest + ".part"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) { // left by an interrupted copy
		return err
	}
	if err := copyRegular(src, tmp, 0o644); err != nil {
		return err
	}
	if got, err := fileSHA256(tmp); err != nil || got != sha { // it changed while copying
		os.Remove(tmp)
		return fmt.Errorf("--kernel %s changed during the copy", src)
	}
	return os.Rename(tmp, dest)
}

// Base implements Sources: the signed SHA256SUMS must name the pinned hash
// before the image downloads, and the image must match it.
func (s PinnedSources) Base(ctx context.Context, dest string) error {
	if cached(dest, s.Ubuntu.SquashFSSHA256) {
		return nil
	}
	sums, err := s.D.Get(ctx, s.Ubuntu.SumsURL, sumsMax)
	if err != nil {
		return err
	}
	sig, err := s.D.Get(ctx, s.Ubuntu.SumsSigURL, sumsMax)
	if err != nil {
		return err
	}
	sum, err := fetch.VerifiedUbuntuSum(sums, sig, s.Ubuntu)
	if err != nil {
		return fmt.Errorf("Ubuntu's signed checksums: %w", err)
	}
	return s.D.Download(ctx, s.Ubuntu.SquashFSURL, sum, dest)
}

// Claude implements Sources.
func (s PinnedSources) Claude(ctx context.Context, dest string) error {
	if cached(dest, s.ClaudePin.BinarySHA256) {
		return nil
	}
	n := fetch.ClaudeCodeBinary(s.D, s.WorkDir)
	n.URL, n.Integrity, n.BinarySHA256 = s.ClaudePin.TarballURL, s.ClaudePin.Integrity, s.ClaudePin.BinarySHA256
	return n.Fetch(ctx, dest)
}

// Plugin implements Sources.
func (s PinnedSources) Plugin(ctx context.Context) ([]byte, error) {
	return s.CodeCheck.Archive(ctx)
}
