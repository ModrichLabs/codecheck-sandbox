package guestinit

import (
	"bytes"
	"io"
	"os"
	"strings"
	"syscall"
)

// mountCall records one Mount.
type mountCall struct {
	Source, Target, FSType string
	Flags                  uintptr
	Data                   string
}

// fakeSys records what the guest init asks of the kernel and keeps written
// files in memory.
type fakeSys struct {
	mounts   []mountCall
	unmounts []string
	dirs     []string
	files    map[string]*fileBuf
	devices  map[string][]byte
	synced   int
	mountErr map[string]error // by target
}

type fileBuf struct {
	bytes.Buffer
	mode   os.FileMode
	closed bool
}

func (f *fileBuf) Close() error { f.closed = true; return nil }

func newFakeSys() *fakeSys {
	return &fakeSys{files: map[string]*fileBuf{}, devices: map[string][]byte{}, mountErr: map[string]error{}}
}

func (s *fakeSys) Mount(source, target, fstype string, flags uintptr, data string) error {
	s.mounts = append(s.mounts, mountCall{source, target, fstype, flags, data})
	return s.mountErr[target]
}

func (s *fakeSys) Unmount(target string) error {
	s.unmounts = append(s.unmounts, target)
	return nil
}

func (s *fakeSys) MkdirAll(path string, _ os.FileMode) error {
	s.dirs = append(s.dirs, path)
	return nil
}

func (s *fakeSys) WriteFile(path string, data []byte, mode os.FileMode) error {
	f := &fileBuf{mode: mode, closed: true}
	f.Write(data)
	s.files[path] = f
	return nil
}

func (s *fakeSys) Create(path string, mode os.FileMode) (io.WriteCloser, error) {
	f := &fileBuf{mode: mode}
	s.files[path] = f
	return f, nil
}

func (s *fakeSys) ReadDevice(path string, n int) ([]byte, error) {
	b, ok := s.devices[path]
	if !ok {
		return nil, syscall.ENOENT
	}
	if len(b) > n {
		b = b[:n]
	}
	return b, nil
}

func (s *fakeSys) Sync() { s.synced++ }

func (s *fakeSys) mountAt(target string) (mountCall, bool) {
	for _, m := range s.mounts {
		if m.Target == target {
			return m, true
		}
	}
	return mountCall{}, false
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}
