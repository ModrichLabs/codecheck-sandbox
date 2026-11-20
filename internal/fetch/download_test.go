package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var payload = []byte(strings.Repeat("firecracker!", 10000))

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func tlsServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	_, _ = w.Write(payload)
}

// leftovers lists every file in dir, temporary ones included.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDownloadGoodHash(t *testing.T) {
	srv := tlsServer(t, serve)
	dir := t.TempDir()
	dest := filepath.Join(dir, "fc.tgz")
	var calls int
	var last int64
	d := Downloader{Client: srv.Client(), Progress: func(done, total int64) { calls++; last = done }}
	if err := d.Download(context.Background(), srv.URL+"/fc.tgz", sum(payload), dest); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("dest holds %d bytes, %v", len(got), err)
	}
	if calls == 0 || last != int64(len(payload)) {
		t.Errorf("progress: %d calls, last %d; want calls ending at %d", calls, last, len(payload))
	}
	if names := leftovers(t, dir); len(names) != 1 {
		t.Errorf("folder holds %v, want only the download", names)
	}
}

func TestDownloadBadHashLeavesNothing(t *testing.T) {
	srv := tlsServer(t, serve)
	dir := t.TempDir()
	dest := filepath.Join(dir, "fc.tgz")
	err := Downloader{Client: srv.Client()}.Download(context.Background(), srv.URL, strings.Repeat("0", 64), dest)
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("a failed download left %v behind", names)
	}
}

func TestDownloadRefusesPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(serve))
	defer srv.Close()
	err := Downloader{Client: srv.Client()}.Download(context.Background(), srv.URL, sum(payload), filepath.Join(t.TempDir(), "x"))
	if !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("err = %v, want ErrNotHTTPS", err)
	}
}

func TestDownloadRefusesARedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(serve))
	defer plain.Close()
	srv := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/fc.tgz", http.StatusFound)
	})
	dir := t.TempDir()
	err := Downloader{Client: srv.Client()}.Download(context.Background(), srv.URL, sum(payload), filepath.Join(dir, "x"))
	if !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("err = %v, want ErrNotHTTPS", err)
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("left %v behind", names)
	}
}

func TestDownloadPartialBody(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload[:len(payload)/2]) // the connection drops half-way
	})
	dir := t.TempDir()
	err := Downloader{Client: srv.Client()}.Download(context.Background(), srv.URL, sum(payload), filepath.Join(dir, "x"))
	if err == nil {
		t.Fatal("a half download succeeded")
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("left %v behind", names)
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "gone", http.StatusNotFound) })
	err := Downloader{Client: srv.Client()}.Download(context.Background(), srv.URL, sum(payload), filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want one naming the 404", err)
	}
}

func TestDownloadHashesTheBytesAsPublished(t *testing.T) {
	// A misconfigured CDN serves a .tgz with Content-Encoding: gzip. The pin
	// covers the file as published, so no transparent decoding may happen.
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(payload)
	w.Close()
	published := gz.Bytes()
	srv := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(published)
	})
	dest := filepath.Join(t.TempDir(), "x.tgz")
	if err := (Downloader{Client: srv.Client()}).Download(context.Background(), srv.URL, sum(published), dest); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, published) {
		t.Errorf("dest holds %d bytes, want the %d published bytes", len(got), len(published))
	}
}

func TestDownloadSizeCap(t *testing.T) {
	srv := tlsServer(t, serve)
	dir := t.TempDir()
	err := Downloader{Client: srv.Client(), MaxBytes: 1024}.Download(context.Background(), srv.URL, sum(payload), filepath.Join(dir, "x"))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("left %v behind", names)
	}
}

func TestDownloadRefusesABadPin(t *testing.T) {
	err := Downloader{}.Download(context.Background(), "https://example.invalid/x", "abc", filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Error("a malformed SHA-256 pin got accepted")
	}
}
