// Package proxy runs the egress proxy (spec §4): the only way out of a review
// VM. It accepts CONNECT to api.anthropic.com:443 from the guest address
// alone, terminates TLS with a leaf from the sandbox CA, lets three API paths
// through, swaps the guest's placeholder key for the real one, and forwards
// over real TLS. The VM never holds a usable key.
package proxy

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/logging"
)

// Reason names why the proxy refused something.
type Reason string

const (
	DeniedSource   Reason = "denied-source"
	DeniedMethod   Reason = "denied-method"
	DeniedHost     Reason = "denied-host"
	DeniedPath     Reason = "denied-path"
	DeniedKey      Reason = "denied-key"
	CapRequests    Reason = "cap-requests"
	CapBytes       Reason = "cap-bytes"
	CapTunnels     Reason = "cap-tunnels"
	FailedTLS      Reason = "tls-handshake"
	FailedUpstream Reason = "upstream-error"
)

// Denial describes one refusal. It never carries header values, so no key,
// real or not, reaches the log.
type Denial struct {
	Time   time.Time `json:"time"`
	Reason Reason    `json:"reason"`
	Client string    `json:"client,omitempty"`
	Method string    `json:"method,omitempty"`
	Host   string    `json:"host,omitempty"`
	Path   string    `json:"path,omitempty"`
}

// maxField caps each logged field, so a guest cannot bloat the log.
const maxField = 256

// DefaultDenyLogSize rotates the denial log at 10 MB.
const DefaultDenyLogSize = 10 << 20

// DenyLog writes one JSON line per denial.
type DenyLog struct {
	mu  sync.Mutex
	w   io.Writer
	c   io.Closer
	now func() time.Time
}

// NewDenyLog writes denials to w.
func NewDenyLog(w io.Writer) *DenyLog { return &DenyLog{w: w, now: time.Now} }

// OpenDenyLog appends to path (normally /var/log/codecheck-sandbox/proxy.jsonl),
// rotating at max bytes. systemd keeps a DynamicUser's logs folder under
// /var/log/private, which only root enters, so the post-run checks read this
// log as root; the files stay 0600.
func OpenDenyLog(path string, max int64) (*DenyLog, error) {
	w, err := logging.OpenRotating(path, max, 0o600)
	if err != nil {
		return nil, err
	}
	return &DenyLog{w: w, c: w, now: time.Now}, nil
}

// Deny records d, filling in the time and cutting long fields.
func (l *DenyLog) Deny(d Denial) {
	d.Time = l.now().UTC()
	d.Client, d.Method, d.Host, d.Path = cut(d.Client), cut(d.Method), cut(d.Host), cut(d.Path)
	line, err := json.Marshal(d)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(line, '\n'))
}

// Close closes the underlying file, if any.
func (l *DenyLog) Close() error {
	if l.c == nil {
		return nil
	}
	return l.c.Close()
}

func cut(s string) string {
	if len(s) > maxField {
		return s[:maxField] + "…"
	}
	return s
}
