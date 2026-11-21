package launch

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

// RunID names one VM run. Only ParseRunID and NewRunID make one, so holding
// a RunID means the text already passed the pattern and can sit in a path.
type RunID string

var runIDPattern = regexp.MustCompile(`^cc-[0-9]{8}-[0-9]{6}-[a-z0-9]{6}$`)

// ParseRunID checks s against cc-YYYYMMDD-HHMMSS-xxxxxx. The pattern leaves
// no room for slashes, dots, spaces or options.
func ParseRunID(s string) (RunID, error) {
	if !runIDPattern.MatchString(s) {
		return "", fmt.Errorf("run id %q: want cc-YYYYMMDD-HHMMSS-xxxxxx", s)
	}
	return RunID(s), nil
}

// NewRunID makes a fresh id from the UTC time and six random characters.
func NewRunID(now time.Time) (RunID, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return ParseRunID("cc-" + now.UTC().Format("20060102-150405") + "-" + string(b))
}

func (id RunID) String() string { return string(id) }

// Dir returns the run's folder, which only the CLI fills (through the
// privilege tool) and only cc-launch reads.
func (id RunID) Dir(p Paths) string { return filepath.Join(p.RunsDir, string(id)) }
