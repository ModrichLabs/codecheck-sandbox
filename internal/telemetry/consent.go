package telemetry

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Files in the telemetry state folder,
// ~/.local/state/codecheck-sandbox/telemetry.
const (
	InstallIDFile = "install-id"
	QueueFile     = "queue.jsonl"
)

// Decision says whether telemetry may run.
type Decision int

const (
	Off Decision = iota
	On
	// Ask means nothing has decided yet and a person sits at the terminal:
	// the setup wizard asks, and every other command treats Ask as Off.
	Ask
)

func (d Decision) String() string {
	return [...]string{"off", "on", "ask"}[d]
}

// ConsentInputs gathers what decides consent.
type ConsentInputs struct {
	DoNotTrack  string // the DO_NOT_TRACK environment variable
	NoTelemetry bool   // --no-telemetry
	AssumeYes   bool   // --yes
	TTY         bool   // stdin and stdout reach a terminal
	Config      *bool  // telemetry in config.toml; nil when unset
}

// Resolve applies spec §11: DO_NOT_TRACK and --no-telemetry always mean off;
// an explicit telemetry setting decides next, even under --yes; with nothing
// stored, only an interactive run without --yes may ask, and everything else
// stays off.
func Resolve(in ConsentInputs) Decision {
	switch {
	case (in.DoNotTrack != "" && in.DoNotTrack != "0") || in.NoTelemetry:
		return Off
	case in.Config != nil && *in.Config:
		return On
	case in.Config != nil:
		return Off
	case in.TTY && !in.AssumeYes:
		return Ask
	}
	return Off
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// EnsureInstallID returns the install id from dir, creating a random UUIDv4
// (mode 0600) when none exists or the file holds anything else.
func EnsureInstallID(fsys system.FS, dir string) (string, error) {
	path := filepath.Join(dir, InstallIDFile)
	if b, err := fsys.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); uuidPattern.MatchString(id) {
			return id, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	id := newUUIDv4()
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating the telemetry folder: %w", err)
	}
	if err := fsys.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := fsys.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("saving the install id: %w", err)
	}
	return id, nil
}

// Forget deletes the install id and any queued events (telemetry off).
func Forget(fsys system.FS, dir string) error {
	var errs []error
	for _, f := range []string{InstallIDFile, QueueFile} {
		if err := fsys.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on Linux
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
