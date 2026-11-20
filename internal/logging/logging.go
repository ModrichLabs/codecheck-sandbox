// Package logging writes the local structured log (spec §11, layer 1): JSON
// lines through the Redactor into a rotating file that never leaves the
// machine, plus a readable copy on the console with --debug.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	charmlog "charm.land/log/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// FileName names the current log file; rotated copies add .1 to .4.
const FileName = "codecheck-sandbox.log"

// Defaults from spec §11: rotate at 10 MB, keep five files in all.
const (
	DefaultMaxSize = 10 << 20
	keepFiles      = 5
)

// Options configure New.
type Options struct {
	// Dir holds the log files, normally ~/.local/state/codecheck-sandbox/log.
	Dir string
	// Redactor strips secrets from every line, in the file and on the console.
	Redactor *system.Redactor
	// Debug lowers the level to Debug and copies every line to Console.
	Debug bool
	// Console receives the --debug copy; nil means os.Stderr.
	Console io.Writer
	// MaxSize caps each file in bytes; zero means DefaultMaxSize.
	MaxSize int64
}

// New opens the log and returns a logger plus a Closer that flushes and
// closes the file.
func New(opts Options) (*slog.Logger, io.Closer, error) {
	if opts.Redactor == nil {
		return nil, nil, errors.New("logging: a Redactor must guard the log")
	}
	if opts.MaxSize <= 0 {
		opts.MaxSize = DefaultMaxSize
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating the log folder: %w", err)
	}
	file, err := openRotating(filepath.Join(opts.Dir, FileName), opts.MaxSize)
	if err != nil {
		return nil, nil, err
	}
	level := slog.LevelInfo
	if opts.Debug {
		level = slog.LevelDebug
	}
	fileOut := opts.Redactor.Writer(file)
	closers := closeAll{fileOut, file}
	var h slog.Handler = slog.NewJSONHandler(fileOut, &slog.HandlerOptions{Level: level})

	if opts.Debug {
		console := opts.Console
		if console == nil {
			console = os.Stderr
		}
		consoleOut := opts.Redactor.Writer(console)
		closers = append(closeAll{consoleOut}, closers...)
		h = fanout{h, charmlog.NewWithOptions(consoleOut, charmlog.Options{Level: charmlog.DebugLevel})}
	}
	return slog.New(h), closers, nil
}

// closeAll closes each in order and reports every failure.
type closeAll []io.Closer

func (c closeAll) Close() error {
	var errs []error
	for _, x := range c {
		errs = append(errs, x.Close())
	}
	return errors.Join(errs...)
}

// fanout sends each record to every handler that wants it.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// rotating appends to path and, before a write would push it past max,
// shifts path to path.1, path.1 to path.2 and so on, dropping the oldest so
// keepFiles files remain.
type rotating struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotating(path string, max int64) (*rotating, error) {
	r := &rotating{path: path, max: max}
	return r, r.open()
}

func (r *rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening the log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotating) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	for i := keepFiles - 1; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", r.path, i)
		newer := r.path
		if i > 1 {
			newer = fmt.Sprintf("%s.%d", r.path, i-1)
		}
		if err := os.Rename(newer, older); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return r.open()
}

func (r *rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
