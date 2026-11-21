package fetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
	if n.MaxBytes <= 0 {
		return errors.New("NpmBinary needs a MaxBytes size cap for its member")
	}
	tgz := filepath.Join(n.WorkDir, path.Base(n.URL))
	if err := n.DownloadSRI(ctx, n.URL, n.Integrity, tgz); err != nil {
		return err
	}
	defer os.Remove(tgz)
	return n.extract(tgz, dest)
}

func (n NpmBinary) extract(tgz, dest string) error {
	return extractMembers(tgz, []member{{name: n.Member, sha: n.BinarySHA256, max: n.MaxBytes, dest: dest}})
}

// member names one file to take from a tarball.
type member struct {
	name, sha string
	max       int64
	dest      string
}

// extractMembers takes each member out of a .tgz into its dest with mode
// 0755. It refuses the whole tarball on any unsafe entry, and moves the
// members into place only once every one has passed its size cap and
// SHA-256, so a bad tarball leaves every dest untouched.
func extractMembers(tgz string, want []member) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(tgz), err)
	}

	tmps := make([]*os.File, len(want))
	defer func() {
		for _, t := range tmps {
			if t != nil {
				t.Close()
				os.Remove(t.Name())
			}
		}
	}()
	hashes := make([]hash.Hash, len(want))
	for i, m := range want {
		if tmps[i], err = os.CreateTemp(filepath.Dir(m.dest), "."+filepath.Base(m.dest)+".part-*"); err != nil {
			return err
		}
		hashes[i] = sha256.New()
	}

	found := make([]bool, len(want))
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
		i := slices.IndexFunc(want, func(m member) bool { return m.name == hdr.Name })
		if i < 0 {
			continue
		}
		if found[i] {
			return fmt.Errorf("tarball holds %s twice", hdr.Name)
		}
		found[i] = true
		written, err := io.Copy(io.MultiWriter(tmps[i], hashes[i]), io.LimitReader(tr, want[i].max+1))
		if err != nil {
			return err
		}
		if written > want[i].max {
			return fmt.Errorf("%s: %w (%d bytes)", hdr.Name, ErrTooLarge, want[i].max)
		}
	}
	for i, m := range want {
		if !found[i] {
			return fmt.Errorf("tarball holds no %s", m.name)
		}
		if got := hex.EncodeToString(hashes[i].Sum(nil)); got != m.sha {
			return fmt.Errorf("%s: %w: got %s, want %s", m.name, ErrHashMismatch, got, m.sha)
		}
		if err := tmps[i].Sync(); err != nil {
			return err
		}
		if err := tmps[i].Chmod(0o755); err != nil {
			return err
		}
	}
	for i := range want {
		if err := tmps[i].Close(); err != nil {
			return err
		}
	}
	var moved []string
	for i, m := range want {
		if err := os.Rename(tmps[i].Name(), m.dest); err != nil {
			// Take back the members already moved, so no dest holds a new
			// file beside an old one.
			for _, d := range moved {
				os.Remove(d)
			}
			return err
		}
		moved = append(moved, m.dest)
		tmps[i] = nil
	}
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
