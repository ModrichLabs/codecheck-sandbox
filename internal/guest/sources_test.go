package guest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/fetch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

// ubuntuServer serves the real signed SHA256SUMS from fetch's testdata, and
// body for the image.
func ubuntuServer(t *testing.T, sums []byte, image []byte) (*httptest.Server, *atomic.Int32, pins.UbuntuPin) {
	t.Helper()
	sig, err := os.ReadFile("../fetch/testdata/ubuntu/SHA256SUMS.gpg")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	img := path.Base(pins.Ubuntu.SquashFSURL)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch path.Base(r.URL.Path) {
		case "SHA256SUMS":
			w.Write(sums)
		case "SHA256SUMS.gpg":
			w.Write(sig)
		case img:
			w.Write(image)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	pin := pins.Ubuntu
	pin.SumsURL, pin.SumsSigURL, pin.SquashFSURL = srv.URL+"/SHA256SUMS", srv.URL+"/SHA256SUMS.gpg", srv.URL+"/"+img
	return srv, &hits, pin
}

func realSums(t *testing.T) []byte {
	b, err := os.ReadFile("../fetch/testdata/ubuntu/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBaseChecksTheSignatureFirst(t *testing.T) {
	tampered := []byte(strings.Replace(string(realSums(t)), pins.Ubuntu.SquashFSSHA256, strings.Repeat("0", 64), 1))
	srv, hits, pin := ubuntuServer(t, tampered, []byte("image"))
	s := PinnedSources{D: fetch.Downloader{Client: srv.Client()}, Ubuntu: pin}
	dest := filepath.Join(t.TempDir(), "base.squashfs")
	if err := s.Base(context.Background(), dest); err == nil {
		t.Fatal("Base accepted a checksum list whose signature fails")
	}
	if hits.Load() != 2 {
		t.Errorf("%d requests; the image must not download before the signature passes", hits.Load())
	}
}

func TestBaseChecksTheImageAgainstThePin(t *testing.T) {
	srv, hits, pin := ubuntuServer(t, realSums(t), []byte("not the pinned image"))
	s := PinnedSources{D: fetch.Downloader{Client: srv.Client()}, Ubuntu: pin}
	dest := filepath.Join(t.TempDir(), "base.squashfs")
	if err := s.Base(context.Background(), dest); !errors.Is(err, fetch.ErrHashMismatch) {
		t.Fatalf("Base = %v, want ErrHashMismatch", err)
	}
	if hits.Load() != 3 {
		t.Errorf("%d requests, want sums, signature, then the image", hits.Load())
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a bad image landed at dest")
	}
}

func TestSourcesReuseACachedFile(t *testing.T) {
	// A cached kernel with the pinned hash needs no network at all.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request for %s", r.URL.Path)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "vmlinux")
	os.WriteFile(dest, kernelBytes, 0o644)
	pin := pins.Kernel
	pin.URL, pin.SHA256 = srv.URL+"/vmlinux", hexSum(kernelBytes)
	s := PinnedSources{D: fetch.Downloader{Client: srv.Client()}, KernelPin: pin}
	if err := s.Kernel(context.Background(), dest); err != nil {
		t.Errorf("Kernel: %v", err)
	}

	// A cached file that differs gets fetched again.
	os.WriteFile(dest, []byte("stale"), 0o644)
	var hits atomic.Int32
	srv2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(kernelBytes)
	}))
	defer srv2.Close()
	pin.URL = srv2.URL + "/vmlinux"
	s = PinnedSources{D: fetch.Downloader{Client: srv2.Client()}, KernelPin: pin}
	if err := s.Kernel(context.Background(), dest); err != nil || hits.Load() != 1 {
		t.Errorf("Kernel = %v after %d requests; want one fresh download", err, hits.Load())
	}
}
