package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDenyLogFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewDenyLog(&buf)
	l.now = func() time.Time { return time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC) }
	l.Deny(Denial{Reason: DeniedHost, Client: "172.16.0.2:40000", Method: "CONNECT", Host: "example.com:443"})

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("line is not JSON: %v\n%s", err, buf.String())
	}
	want := map[string]any{
		"time": "2026-11-20T04:00:00Z", "reason": "denied-host",
		"client": "172.16.0.2:40000", "method": "CONNECT", "host": "example.com:443",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["path"]; ok {
		t.Error("an empty path should stay out of the line")
	}
	if !strings.HasSuffix(buf.String(), "}\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("want exactly one line, got %q", buf.String())
	}
}

func TestDenyLogTruncatesLongFields(t *testing.T) {
	var buf bytes.Buffer
	NewDenyLog(&buf).Deny(Denial{Reason: DeniedPath, Path: "/" + strings.Repeat("x", 5000)})
	if buf.Len() > 1024 {
		t.Errorf("one denial wrote %d bytes; long fields must get cut", buf.Len())
	}
}

func TestDenyLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.jsonl")
	l, err := OpenDenyLog(path, 512)
	if err != nil {
		t.Fatalf("OpenDenyLog: %v", err)
	}
	for range 30 {
		l.Deny(Denial{Reason: DeniedHost, Client: "172.16.0.2:1", Host: "example.com:443"})
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("no rotated file: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Size() > 512 || info.Mode().Perm() != 0o600 {
		t.Errorf("current file: %d bytes, mode %v; want at most 512 and 0600", info.Size(), info.Mode().Perm())
	}
}
