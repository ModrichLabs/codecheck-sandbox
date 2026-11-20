package fetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

// ErrTooLarge reports a tarball member past its size cap.
var ErrTooLarge = errors.New("file exceeds its size cap")

// claudeMaxBytes caps the Claude Code binary (355.6 MB at 2.1.327).
const claudeMaxBytes = 512 << 20

// NpmBinary takes one file from a pinned npm tarball without npm, Node or any
// package script: it downloads the tarball, checks its integrity, refuses the
// whole tarball if any entry looks unsafe, and checks the file's own SHA-256.
type NpmBinary struct {
	Downloader
	URL          string
	Integrity    string // npm dist.integrity of the tarball
	Member       string // the file to take, such as package/claude
	BinarySHA256 string
	MaxBytes     int64
	WorkDir      string // holds the tarball while it gets read
}

// ClaudeCodeBinary describes the pinned Claude Code binary.
func ClaudeCodeBinary(d Downloader, workDir string) NpmBinary {
	return NpmBinary{
		Downloader:   d,
		URL:          pins.ClaudeCode.TarballURL,
		Integrity:    pins.ClaudeCode.Integrity,
		Member:       "package/claude",
		BinarySHA256: pins.ClaudeCode.BinarySHA256,
		MaxBytes:     claudeMaxBytes,
		WorkDir:      workDir,
	}
}

// Fetch writes the member to dest with mode 0755, or leaves dest untouched.
func (n NpmBinary) Fetch(ctx context.Context, dest string) error {
	tgz := filepath.Join(n.WorkDir, path.Base(n.URL))
	if err := n.DownloadSRI(ctx, n.URL, n.Integrity, tgz); err != nil {
		return err
	}
	defer os.Remove(tgz)
	return n.extract(tgz, dest)
}

func (n NpmBinary) extract(tgz, dest string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(tgz), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".part-*")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	h := sha256.New()
	found := false
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", filepath.Base(tgz), err)
		}
		if err := safeEntry(hdr); err != nil {
			return err
		}
		if hdr.Name != n.Member {
			continue
		}
		if found {
			return fmt.Errorf("tarball holds %s twice", n.Member)
		}
		found = true
		written, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(tr, n.MaxBytes+1))
		if err != nil {
			return err
		}
		if written > n.MaxBytes {
			return fmt.Errorf("%s: %w (%d bytes)", n.Member, ErrTooLarge, n.MaxBytes)
		}
	}
	if !found {
		return fmt.Errorf("tarball holds no %s", n.Member)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != n.BinarySHA256 {
		return fmt.Errorf("%s: %w: got %s, want %s", n.Member, ErrHashMismatch, got, n.BinarySHA256)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	keep = true
	return nil
}

// safeEntry refuses absolute paths, any ".." component, and every entry type
// other than a regular file or a folder (links above all).
func safeEntry(h *tar.Header) error {
	if path.IsAbs(h.Name) || slices.Contains(strings.Split(h.Name, "/"), "..") {
		return fmt.Errorf("tarball entry %q escapes its folder", h.Name)
	}
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeDir:
		return nil
	}
	return fmt.Errorf("tarball entry %q has type %q; only files and folders allowed", h.Name, h.Typeflag)
}
