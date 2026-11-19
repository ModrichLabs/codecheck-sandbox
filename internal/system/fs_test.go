package system

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

// testFS runs the same contract against every FS implementation. root names a
// directory that already exists and stays empty at the start.
func testFS(t *testing.T, newFS func(t *testing.T) (FS, string)) {
	t.Run("missing file", func(t *testing.T) {
		f, root := newFS(t)
		if _, err := f.ReadFile(filepath.Join(root, "nope")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile err = %v, want ErrNotExist", err)
		}
		if _, err := f.Stat(filepath.Join(root, "nope")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat err = %v, want ErrNotExist", err)
		}
		if err := f.Remove(filepath.Join(root, "nope")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Remove err = %v, want ErrNotExist", err)
		}
	})

	t.Run("write, read and stat", func(t *testing.T) {
		f, root := newFS(t)
		p := filepath.Join(root, "key")
		if err := f.WriteFile(p, []byte("sk-test"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := f.ReadFile(p)
		if err != nil || string(got) != "sk-test" {
			t.Fatalf("ReadFile = %q, %v; want sk-test", got, err)
		}
		info, err := f.Stat(p)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.IsDir() || info.Size() != 7 || info.Mode().Perm() != 0o600 || info.Name() != "key" {
			t.Errorf("Stat = dir %v, size %d, perm %v, name %q", info.IsDir(), info.Size(), info.Mode().Perm(), info.Name())
		}
	})

	t.Run("overwrite keeps the mode", func(t *testing.T) {
		f, root := newFS(t)
		p := filepath.Join(root, "conf")
		_ = f.WriteFile(p, []byte("one"), 0o600)
		if err := f.WriteFile(p, []byte("two!"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, _ := f.ReadFile(p)
		info, _ := f.Stat(p)
		if string(got) != "two!" || info.Mode().Perm() != 0o600 {
			t.Errorf("after overwrite: %q, perm %v; want two!, 0600", got, info.Mode().Perm())
		}
	})

	t.Run("write needs the parent folder", func(t *testing.T) {
		f, root := newFS(t)
		err := f.WriteFile(filepath.Join(root, "a", "b"), []byte("x"), 0o600)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want ErrNotExist", err)
		}
	})

	t.Run("mkdir all", func(t *testing.T) {
		f, root := newFS(t)
		d := filepath.Join(root, "a", "b", "c")
		if err := f.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := f.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("second MkdirAll: %v", err)
		}
		for _, p := range []string{filepath.Join(root, "a"), d} {
			info, err := f.Stat(p)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Errorf("Stat(%s) = %v, %v; want a 0700 folder", p, info, err)
			}
		}
		if err := f.WriteFile(filepath.Join(d, "f"), []byte("x"), 0o600); err != nil {
			t.Errorf("WriteFile inside the new folder: %v", err)
		}
	})

	t.Run("mkdir over a file fails", func(t *testing.T) {
		f, root := newFS(t)
		p := filepath.Join(root, "file")
		_ = f.WriteFile(p, []byte("x"), 0o600)
		if err := f.MkdirAll(filepath.Join(p, "sub"), 0o700); err == nil {
			t.Error("MkdirAll through a file: want an error")
		}
	})

	t.Run("remove", func(t *testing.T) {
		f, root := newFS(t)
		d := filepath.Join(root, "d")
		p := filepath.Join(d, "f")
		_ = f.MkdirAll(d, 0o700)
		_ = f.WriteFile(p, []byte("x"), 0o600)
		if err := f.Remove(d); err == nil {
			t.Error("Remove of a non-empty folder: want an error")
		}
		if err := f.Remove(p); err != nil {
			t.Fatalf("Remove file: %v", err)
		}
		if _, err := f.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat after Remove: %v, want ErrNotExist", err)
		}
		if err := f.Remove(d); err != nil {
			t.Errorf("Remove of an empty folder: %v", err)
		}
	})
}

func TestOSFS(t *testing.T) {
	testFS(t, func(t *testing.T) (FS, string) { return OSFS{}, t.TempDir() })
}

func TestMemFS(t *testing.T) {
	testFS(t, func(t *testing.T) (FS, string) {
		m := NewMemFS()
		if err := m.MkdirAll("/tmp/root", 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		return m, "/tmp/root"
	})
}

func TestMemFSResolvesRelativePathsFromRoot(t *testing.T) {
	m := NewMemFS()
	if err := m.WriteFile("config.toml", []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile with a relative path: %v", err)
	}
	if got, err := m.ReadFile("/config.toml"); err != nil || string(got) != "x" {
		t.Errorf("ReadFile(/config.toml) = %q, %v; want x", got, err)
	}
}

func TestMemFSReadReturnsACopy(t *testing.T) {
	m := NewMemFS()
	_ = m.WriteFile("/f", []byte("abc"), 0o600)
	got, _ := m.ReadFile("/f")
	got[0] = 'X'
	again, _ := m.ReadFile("/f")
	if string(again) != "abc" {
		t.Errorf("MemFS content changed through a returned slice: %q", again)
	}
}
