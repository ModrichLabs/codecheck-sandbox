package system

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"slices"
	"sync"
)

// redacted replaces every registered secret.
const redacted = "[redacted]"

// Redactor replaces registered secrets, such as the API key, wherever they
// would reach a log, an error message or a support bundle. Its zero value
// works; it suits concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets [][]byte // longest first
}

// Add registers a secret. Empty secrets get ignored.
func (r *Redactor) Add(secret string) {
	if secret == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, []byte(secret))
	slices.SortStableFunc(r.secrets, func(a, b []byte) int { return cmp.Compare(len(b), len(a)) })
}

// Redact returns a copy of b with every secret replaced. It marks every match
// in the original bytes first and merges spans that touch or overlap, so two
// overlapping secrets never leave a fragment of either behind.
func (r *Redactor) Redact(b []byte) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hidden := make([]bool, len(b))
	found := false
	for _, s := range r.secrets {
		for i := 0; ; {
			j := bytes.Index(b[i:], s)
			if j < 0 {
				break
			}
			for k := i + j; k < i+j+len(s); k++ {
				hidden[k] = true
			}
			found = true
			i += j + 1
		}
	}
	if !found {
		return bytes.Clone(b)
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if !hidden[i] {
			out = append(out, b[i])
			continue
		}
		out = append(out, redacted...)
		for i+1 < len(b) && hidden[i+1] {
			i++
		}
	}
	return out
}

// RedactString does what Redact does, for a string.
func (r *Redactor) RedactString(s string) string { return string(r.Redact([]byte(s))) }

// partialSuffix returns the length of the longest tail of b that could start
// a secret, so a writer can hold it back until the next write decides.
func (r *Redactor) partialSuffix(b []byte) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := 0
	for _, s := range r.secrets {
		for k := min(len(s)-1, len(b)); k > best; k-- {
			if bytes.HasSuffix(b, s[:k]) {
				best = k
				break
			}
		}
	}
	return best
}

// Writer returns a writer that redacts everything it passes to w, including
// a secret split across several writes. It holds back only a tail that could
// start a secret, so whole log lines go through at once. Close flushes that
// tail; it does not close w.
func (r *Redactor) Writer(w io.Writer) io.WriteCloser {
	return &redactingWriter{r: r, w: w}
}

type redactingWriter struct {
	mu      sync.Mutex
	r       *Redactor
	w       io.Writer
	pending []byte
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	buf := rw.r.Redact(append(bytes.Clone(rw.pending), p...))
	keep := rw.r.partialSuffix(buf)
	if _, err := rw.w.Write(buf[:len(buf)-keep]); err != nil {
		return 0, err // pending keeps its earlier bytes, so a retry of p starts clean
	}
	rw.pending = bytes.Clone(buf[len(buf)-keep:])
	return len(p), nil
}

func (rw *redactingWriter) Close() error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	buf := rw.r.Redact(rw.pending)
	rw.pending = nil
	_, err := rw.w.Write(buf)
	return err
}

// Runner wraps inner so that error messages and stderr, captured or streamed,
// never carry a secret. Stdout stays untouched, since callers may need to
// read a secret back.
func (r *Redactor) Runner(inner Runner) Runner { return redactingRunner{r: r, inner: inner} }

type redactingRunner struct {
	r     *Redactor
	inner Runner
}

func (rr redactingRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	var streamed io.WriteCloser
	if c.Stderr != nil {
		streamed = rr.r.Writer(c.Stderr)
		c.Stderr = streamed
	}
	res, err := rr.inner.Run(ctx, c)
	if streamed != nil {
		if cerr := streamed.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	res.Stderr = rr.r.Redact(res.Stderr)
	if err != nil {
		err = &redactedError{msg: rr.r.RedactString(err.Error()), err: err}
	}
	return res, err
}

// redactedError shows a redacted message but keeps the original chain for
// errors.Is and errors.As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
