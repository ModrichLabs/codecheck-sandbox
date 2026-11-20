package launch

import (
	"strings"
	"testing"
	"time"
)

func TestParseRunIDAcceptsTheFormat(t *testing.T) {
	id, err := ParseRunID("cc-20261121-101500-a1b2c3")
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	if got, want := id.Dir(DefaultPaths()), "/var/lib/codecheck-sandbox/runs/cc-20261121-101500-a1b2c3"; got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	if id.String() != "cc-20261121-101500-a1b2c3" {
		t.Errorf("String = %q", id)
	}
}

func TestParseRunIDRefusesEverythingElse(t *testing.T) {
	for _, s := range []string{
		"",
		"cc-20261121-101500",
		"cc-20261121-101500-A1B2C3",
		"cc-20261121-101500-a1b2c3d",
		"cc-2026112-101500-a1b2c3",
		"xx-20261121-101500-a1b2c3",
		"../cc-20261121-101500-a1b2c3",
		"cc-20261121-101500-a1b2c3/../../etc",
		"cc-20261121-101500-a1b2c3\n",
		" cc-20261121-101500-a1b2c3",
		"cc-20261121-101500-a1b2c3/",
		"--config-file=/etc/shadow",
		"cc-20261121-101500-a1b2c" + "٣", // a non-ASCII digit
	} {
		if id, err := ParseRunID(s); err == nil {
			t.Errorf("ParseRunID(%q) = %q, want an error", s, id)
		}
	}
}

func TestNewRunIDParses(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 15, 0, 0, time.UTC)
	seen := map[RunID]bool{}
	for range 50 {
		id, err := NewRunID(now)
		if err != nil {
			t.Fatalf("NewRunID: %v", err)
		}
		if !strings.HasPrefix(id.String(), "cc-20261121-101500-") {
			t.Errorf("NewRunID = %q, want the UTC time in it", id)
		}
		if _, err := ParseRunID(id.String()); err != nil {
			t.Errorf("NewRunID made %q, which ParseRunID refuses: %v", id, err)
		}
		seen[id] = true
	}
	if len(seen) < 45 {
		t.Errorf("50 ids gave only %d distinct values", len(seen))
	}
}
