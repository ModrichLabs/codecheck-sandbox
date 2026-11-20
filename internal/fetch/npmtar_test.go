package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

var claudeBinary = []byte("\x7fELF pretend claude binary")

type entry struct {
	name, body, link string
	typ              byte
}

func tgz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: typ, Linkname: e.link}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sri(b []byte) string {
	h := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}

func goodTarball(t *testing.T) []byte {
	return tgz(t,
		entry{name: "package/package.json", body: `{"name":"@anthropic-ai/claude-code-linux-x64"}`},
		entry{name: "package/claude", body: string(claudeBinary)},
	)
}

func npmServer(t *testing.T, body []byte) (string, *http.Client) {
	t.Helper()
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	return srv.URL + "/claude-code-linux-x64-2.1.327.tgz", srv.Client()
}

func fetchClaude(t *testing.T, body []byte, integrity, binSHA string, max int64) (string, error) {
	t.Helper()
	url, srvClient := npmServer(t, body)
	dir := t.TempDir()
	dest := filepath.Join(dir, "claude")
	n := NpmBinary{
		Downloader: Downloader{Client: srvClient},
		URL:        url, Integrity: integrity, BinarySHA256: binSHA,
		Member: "package/claude", MaxBytes: max, WorkDir: dir,
	}
	return dest, n.Fetch(context.Background(), dest)
}

func TestNpmBinaryGood(t *testing.T) {
	body := goodTarball(t)
	dest, err := fetchClaude(t, body, sri(body), sum(claudeBinary), 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, _ := os.ReadFile(dest)
	info, _ := os.Stat(dest)
	if !bytes.Equal(got, claudeBinary) || info.Mode().Perm() != 0o755 {
		t.Errorf("dest = %q mode %v, want the binary with mode 0755", got, info.Mode().Perm())
	}
	if names := leftovers(t, filepath.Dir(dest)); len(names) != 1 {
		t.Errorf("work folder holds %v, want only the binary (the tarball removed)", names)
	}
}

func TestNpmBinaryBadIntegrity(t *testing.T) {
	body := goodTarball(t)
	dest, err := fetchClaude(t, body, sri([]byte("something else")), sum(claudeBinary), 1<<20)
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("err = %v, want ErrHashMismatch", err)
	}
	if names := leftovers(t, filepath.Dir(dest)); len(names) != 0 {
		t.Errorf("left %v behind", names)
	}
}

func TestNpmBinaryBadBinaryHash(t *testing.T) {
	body := goodTarball(t)
	dest, err := fetchClaude(t, body, sri(body), sum([]byte("other")), 1<<20)
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("err = %v, want ErrHashMismatch", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a binary with the wrong hash landed at dest")
	}
}

func TestNpmBinaryRefusesUnsafeEntries(t *testing.T) {
	tests := map[string][]entry{
		"path traversal": {{name: "package/../../etc/cron.d/x", body: "evil"}, {name: "package/claude", body: string(claudeBinary)}},
		"absolute path":  {{name: "/etc/passwd", body: "evil"}, {name: "package/claude", body: string(claudeBinary)}},
		"symlink":        {{name: "package/claude", typ: tar.TypeSymlink, link: "/bin/sh"}},
		"hard link":      {{name: "package/x", body: "x"}, {name: "package/claude", typ: tar.TypeLink, link: "package/x"}},
		"two binaries":   {{name: "package/claude", body: string(claudeBinary)}, {name: "package/claude", body: "second"}},
		"no binary":      {{name: "package/package.json", body: "{}"}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			body := tgz(t, entries...)
			dest, err := fetchClaude(t, body, sri(body), sum(claudeBinary), 1<<20)
			if err == nil {
				t.Fatal("an unsafe tarball got accepted")
			}
			if _, statErr := os.Stat(dest); statErr == nil {
				t.Error("dest exists after a refusal")
			}
		})
	}
}

func TestNpmBinarySizeCap(t *testing.T) {
	body := goodTarball(t)
	if _, err := fetchClaude(t, body, sri(body), sum(claudeBinary), 8); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}
