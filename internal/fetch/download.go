package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// ErrHashMismatch reports a download whose SHA-256 differs from its pin.
var ErrHashMismatch = errors.New("SHA-256 differs from the pin")

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// progressEvery bounds how often a download reports progress.
const progressEvery = 1 << 20

// Downloader fetches pinned files over HTTPS.
type Downloader struct {
	// Client sends the requests; nil means a default client. Downloader
	// adds its own redirect rule to a copy, so redirects stay on HTTPS.
	Client *http.Client
	// Progress hears bytes done and the total (-1 when unknown); nil sends
	// progress to the step engine through step.ReportProgress.
	Progress func(done, total int64)
	// MaxBytes caps one download, so a broken or hostile server cannot fill
	// the disk before the hash check; zero means DefaultMaxDownload.
	MaxBytes int64
}

// DefaultMaxDownload comfortably covers the largest pinned file.
const DefaultMaxDownload = 4 << 30

// Download fetches rawURL into dest, but only if its SHA-256 matches want.
// It writes to a temporary file beside dest, checks the hash, then renames;
// on any failure the temporary file goes and dest stays untouched.
func (d Downloader) Download(ctx context.Context, rawURL, want, dest string) error {
	if !sha256Pattern.MatchString(want) {
		return fmt.Errorf("SHA-256 pin %q: want 64 lowercase hex characters", want)
	}
	return d.fetchTo(ctx, rawURL, dest, sha256.New(), func(sum []byte) error {
		if got := hex.EncodeToString(sum); got != want {
			return fmt.Errorf("%s: %w: got %s, want %s", filepath.Base(dest), ErrHashMismatch, got, want)
		}
		return nil
	})
}

// DownloadSRI works like Download, but checks an npm-style integrity string
// ("sha512-" and the base64 digest) instead of a SHA-256.
func (d Downloader) DownloadSRI(ctx context.Context, rawURL, integrity, dest string) error {
	b64, ok := strings.CutPrefix(integrity, "sha512-")
	want, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil || len(want) != sha512.Size {
		return fmt.Errorf("integrity %q: want sha512- and a base64 SHA-512", integrity)
	}
	return d.fetchTo(ctx, rawURL, dest, sha512.New(), func(sum []byte) error {
		if !bytes.Equal(sum, want) {
			return fmt.Errorf("%s: %w: integrity differs from %s", filepath.Base(dest), ErrHashMismatch, integrity)
		}
		return nil
	})
}

// fetchTo downloads rawURL into a temporary file beside dest while hashing it
// with h, and renames the file into place only when check accepts the sum.
func (d Downloader) fetchTo(ctx context.Context, rawURL, dest string, h hash.Hash, check func(sum []byte) error) error {
	if err := httpsOnly(rawURL); err != nil {
		return err
	}
	client := http.Client{}
	if d.Client != nil {
		client = *d.Client
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return httpsOnly(req.URL.String())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	// The pins cover files as published. Asking for identity stops Go's
	// transport from decoding a Content-Encoding: gzip reply behind our back.
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", filepath.Base(dest), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %s", filepath.Base(dest), resp.Status)
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

	limit := d.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxDownload
	}
	pw := &progressWriter{report: d.report(ctx, filepath.Base(dest)), total: resp.ContentLength}
	n, err := io.Copy(io.MultiWriter(tmp, h, pw), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("downloading %s: %w", filepath.Base(dest), err)
	}
	if n > limit {
		return fmt.Errorf("%s: %w (%d bytes)", filepath.Base(dest), ErrTooLarge, limit)
	}
	pw.flush()
	if err := check(h.Sum(nil)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	keep = true
	return nil
}

func (d Downloader) report(ctx context.Context, name string) func(done, total int64) {
	if d.Progress != nil {
		return d.Progress
	}
	return func(done, total int64) {
		frac := 0.0
		if total > 0 {
			frac = float64(done) / float64(total)
		}
		step.ReportProgress(ctx, "downloading "+name, frac)
	}
}

func httpsOnly(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q: %w", rawURL, ErrNotHTTPS)
	}
	return nil
}

// progressWriter counts bytes and reports at most once per progressEvery.
type progressWriter struct {
	report       func(done, total int64)
	total        int64
	done, posted int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.done-p.posted >= progressEvery {
		p.flush()
	}
	return len(b), nil
}

func (p *progressWriter) flush() {
	if p.done != p.posted || p.done == 0 {
		p.posted = p.done
		p.report(p.done, p.total)
	}
}
