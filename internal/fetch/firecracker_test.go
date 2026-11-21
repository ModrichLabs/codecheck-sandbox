package fetch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

var (
	fcBinary     = []byte("\x7fELF pretend firecracker")
	jailerBinary = []byte("\x7fELF pretend jailer")
)

func fcTarball(t *testing.T, extra ...entry) []byte {
	return tgz(t, append([]entry{
		{name: "release-v1.18.0-x86_64/", typ: '5'},
		{name: "release-v1.18.0-x86_64/firecracker-v1.18.0-x86_64", body: string(fcBinary)},
		{name: "release-v1.18.0-x86_64/jailer-v1.18.0-x86_64", body: string(jailerBinary)},
		{name: "release-v1.18.0-x86_64/SHA256SUMS", body: "whatever"},
	}, extra...)...)
}

func fetchFirecracker(t *testing.T, body []byte, pin pins.FirecrackerPin) (string, error) {
	t.Helper()
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	dir := t.TempDir()
	pin.URL = srv.URL + "/firecracker-v1.18.0-x86_64.tgz"
	r := FirecrackerRelease{Downloader: Downloader{Client: srv.Client()}, Pin: pin, WorkDir: dir}
	return dir, r.Fetch(context.Background(), filepath.Join(dir, "firecracker"), filepath.Join(dir, "jailer"))
}

func goodPin(body []byte) pins.FirecrackerPin {
	return pins.FirecrackerPin{Version: "v1.18.0", SHA256: sum(body),
		FirecrackerSHA256: sum(fcBinary), JailerSHA256: sum(jailerBinary)}
}

func TestFirecrackerReleaseGood(t *testing.T) {
	body := fcTarball(t)
	dir, err := fetchFirecracker(t, body, goodPin(body))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for name, want := range map[string][]byte{"firecracker": fcBinary, "jailer": jailerBinary} {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		info, _ := os.Stat(filepath.Join(dir, name))
		if !bytes.Equal(got, want) || info.Mode().Perm() != 0o755 {
			t.Errorf("%s = %q mode %v", name, got, info.Mode().Perm())
		}
	}
	if names := leftovers(t, dir); len(names) != 2 {
		t.Errorf("work folder holds %v, want only the two binaries", names)
	}
}

func TestFirecrackerReleaseChecksEachBinary(t *testing.T) {
	body := fcTarball(t)
	pin := goodPin(body)
	pin.JailerSHA256 = sum([]byte("another jailer"))
	dir, err := fetchFirecracker(t, body, pin)
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("left %v behind after a bad jailer", names)
	}
}

func TestFirecrackerReleaseChecksTheTarball(t *testing.T) {
	body := fcTarball(t)
	pin := goodPin(body)
	pin.SHA256 = sum([]byte("another tarball"))
	if _, err := fetchFirecracker(t, body, pin); !errors.Is(err, ErrHashMismatch) {
		t.Errorf("err = %v, want ErrHashMismatch", err)
	}
}

func TestFirecrackerReleaseRefusesUnsafeEntries(t *testing.T) {
	body := fcTarball(t, entry{name: "release-v1.18.0-x86_64/evil", link: "/etc/shadow", typ: '2'})
	if _, err := fetchFirecracker(t, body, goodPin(body)); err == nil {
		t.Error("Fetch accepted a tarball holding a symlink")
	}
}
