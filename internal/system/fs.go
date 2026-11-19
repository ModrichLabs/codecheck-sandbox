package system

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FS covers the user-owned file operations the engine needs. Root-owned files
// go through WriteRootFile and the Runner instead. Errors match the os
// package's, so errors.Is(err, fs.ErrNotExist) works for every implementation.
type FS interface {
	ReadFile(name string) ([]byte, error)
	// WriteFile creates or truncates name. An existing file keeps its mode.
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
	MkdirAll(path string, perm fs.FileMode) error
	// Remove deletes a file or an empty folder.
	Remove(name string) error
}

// OSFS implements FS on the real filesystem.
type OSFS struct{}

func (OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (OSFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (OSFS) Stat(name string) (fs.FileInfo, error)        { return os.Stat(name) }
func (OSFS) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (OSFS) Remove(name string) error                     { return os.Remove(name) }

// MemFS implements FS in memory, for tests. It starts with an empty root
// folder "/" and ignores the umask.
type MemFS struct {
	mu      sync.Mutex
	entries map[string]*memEntry
}

type memEntry struct {
	data []byte
	mode fs.FileMode // includes fs.ModeDir for folders
}

// NewMemFS returns an empty MemFS.
func NewMemFS() *MemFS {
	return &MemFS{entries: map[string]*memEntry{"/": {mode: fs.ModeDir | 0o755}}}
}

func memErr(op, name string, err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }

func (m *MemFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[filepath.Clean(name)]
	switch {
	case !ok:
		return nil, memErr("open", name, fs.ErrNotExist)
	case e.mode.IsDir():
		return nil, memErr("read", name, syscall.EISDIR)
	}
	return append([]byte(nil), e.data...), nil
}

func (m *MemFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(name)
	if e, ok := m.entries[p]; ok {
		if e.mode.IsDir() {
			return memErr("open", name, syscall.EISDIR)
		}
		e.data = append([]byte(nil), data...)
		return nil
	}
	parent, ok := m.entries[filepath.Dir(p)]
	switch {
	case !ok:
		return memErr("open", name, fs.ErrNotExist)
	case !parent.mode.IsDir():
		return memErr("open", name, syscall.ENOTDIR)
	}
	m.entries[p] = &memEntry{data: append([]byte(nil), data...), mode: perm.Perm()}
	return nil
}

func (m *MemFS) Stat(name string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(name)
	e, ok := m.entries[p]
	if !ok {
		return nil, memErr("stat", name, fs.ErrNotExist)
	}
	return memInfo{name: filepath.Base(p), size: int64(len(e.data)), mode: e.mode}, nil
}

func (m *MemFS) MkdirAll(path string, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	// Walk from the top down, creating each missing folder.
	var chain []string
	for d := p; ; d = filepath.Dir(d) {
		chain = append(chain, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		d := chain[i]
		if e, ok := m.entries[d]; ok {
			if !e.mode.IsDir() {
				return memErr("mkdir", path, syscall.ENOTDIR)
			}
			continue
		}
		m.entries[d] = &memEntry{mode: fs.ModeDir | perm.Perm()}
	}
	return nil
}

func (m *MemFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(name)
	e, ok := m.entries[p]
	if !ok {
		return memErr("remove", name, fs.ErrNotExist)
	}
	if e.mode.IsDir() {
		prefix := strings.TrimSuffix(p, "/") + "/"
		for k := range m.entries {
			if k != p && strings.HasPrefix(k, prefix) {
				return memErr("remove", name, syscall.ENOTEMPTY)
			}
		}
	}
	delete(m.entries, p)
	return nil
}

type memInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return i.mode }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.mode.IsDir() }
func (i memInfo) Sys() any           { return nil }
