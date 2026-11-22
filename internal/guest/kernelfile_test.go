package guest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/fetch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

// goneServer answers 404 for everything, as a vanished upstream would.
func goneServer(t *testing.T) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKernelDownloadFailureSuggestsTheFlag(t *testing.T) {
	srv := goneServer(t)
	pin := pins.Kernel
	pin.URL, pin.SHA256 = srv.URL+"/vmlinux", hexSum(kernelBytes)
	s := PinnedSources{D: fetch.Downloader{Client: srv.Client()}, KernelPin: pin}
	err := s.Kernel(context.Background(), filepath.Join(t.TempDir(), "vmlinux"))
	if err == nil || !strings.Contains(err.Error(), "--kernel") || !errors.Is(err, ErrKernelUnavailable) {
		t.Errorf("Kernel = %v; want a hint at --kernel", err)
	}
}

func TestKernelFileWithThePinnedHashInstalls(t *testing.T) {
	srv := goneServer(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "my-vmlinux")
	os.WriteFile(file, kernelBytes, 0o644)
	pin := pins.Kernel
	pin.URL, pin.SHA256 = srv.URL+"/vmlinux", hexSum(kernelBytes)
	s := PinnedSources{D: fetch.Downloader{Client: srv.Client()}, KernelPin: pin, KernelFile: file}
	dest := filepath.Join(dir, "cache", "vmlinux")
	os.MkdirAll(filepath.Dir(dest), 0o700)
	if err := s.Kernel(context.Background(), dest); err != nil {
		t.Fatalf("Kernel: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(kernelBytes) {
		t.Error("the kernel file didn't reach the cache")
	}
}

func TestKernelFileWithAnyOtherHashGetsRefused(t *testing.T) {
	srv := goneServer(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "my-vmlinux")
	os.WriteFile(file, []byte("not the pinned kernel"), 0o644)
	pin := pins.Kernel
	pin.URL, pin.SHA256 = srv.URL+"/vmlinux", hexSum(kernelBytes)
	s := PinnedSources{D: fetch.Downloader{Client: srv.Client()}, KernelPin: pin, KernelFile: file}
	dest := filepath.Join(dir, "vmlinux")
	err := s.Kernel(context.Background(), dest)
	if err == nil || !strings.Contains(err.Error(), pin.SHA256) {
		t.Errorf("Kernel = %v; want a refusal naming the pinned hash", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a mismatched kernel reached the cache")
	}
}
