// Package repocheck holds checks that span the whole repository.
package repocheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Raw bidirectional controls make code display differently from how the
// compiler reads it ("Trojan Source", CVE-2021-42574). Source spells them
// as escapes (\u202e) instead.
func TestNoRawBidiControls(t *testing.T) {
	bad, err := bidiFiles(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range bad {
		t.Errorf("%s holds a raw bidirectional control; write it as an escape such as \\u202e", f)
	}
}

func TestBidiFilesFindsThem(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ok.go"), []byte("s := \"\\u202e\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.go"), []byte("s := \"a\u202eb\"\n"), 0o644)
	os.Mkdir(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "bad.md"), []byte("x\u2066y\n"), 0o644)
	got, err := bidiFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != filepath.Join(dir, "bad.go")+","+filepath.Join(dir, "docs", "bad.md") {
		t.Errorf("bidiFiles = %v", got)
	}
}
