package guest

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// maxPluginBytes caps the unpacked CodeCheck plugin.
const maxPluginBytes = 64 << 20

// Inputs names what the builder VM takes from the host.
type Inputs struct {
	Claude    string // the verified Claude Code binary
	GuestInit string // the static cc-guestinit binary
	CACert    []byte // the sandbox CA certificate (PEM)
	Plugin    []byte // `git archive` output of the CodeCheck plugin
}

// StageInputs lays the inputs out in dir under the names guestinit.Build
// expects: claude, cc-guestinit, cc-claude, cc-selftest, ca.crt and plugin/.
func StageInputs(dir string, in Inputs) error {
	for _, c := range []struct{ src, name string }{{in.Claude, "claude"}, {in.GuestInit, "cc-guestinit"}} {
		if err := copyRegular(c.src, filepath.Join(dir, c.name), 0o755); err != nil {
			return err
		}
	}
	for _, s := range []string{"cc-claude", "cc-selftest"} {
		b, err := assets.Guest.ReadFile("guest/" + s)
		if err != nil {
			return err
		}
		if err := writeNew(filepath.Join(dir, s), b, 0o755); err != nil {
			return err
		}
	}
	if err := writeNew(filepath.Join(dir, "ca.crt"), in.CACert, 0o644); err != nil {
		return err
	}
	return untarPlugin(in.Plugin, filepath.Join(dir, "plugin"))
}

// MakeInputs stages the inputs in a fresh folder under work, packs them into
// an ext4 disk at disk with mkfs.ext4 -d (no root needed), and removes the
// folder again.
func MakeInputs(ctx context.Context, r system.Runner, work, disk string, in Inputs) error {
	staging := filepath.Join(work, "inputs")
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := StageInputs(staging, in); err != nil {
		return err
	}
	size, err := treeSize(staging)
	if err != nil {
		return err
	}
	// Room for ext4's own metadata: a quarter more, and 32 MiB besides.
	size = (size + size/4 + 32<<20 + 4095) / 4096 * 4096
	if err := os.Remove(disk); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(disk, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	mkfs, err := system.LookTool("mkfs.ext4")
	if err != nil {
		return err
	}
	res, err := r.Run(ctx, system.Cmd{Name: mkfs, Args: []string{"-q", "-F", "-L", "cc-inputs", "-d", staging, disk}, CleanEnv: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("mkfs.ext4: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// untarPlugin unpacks a git archive into dir, refusing anything but plain
// files and folders inside it, and more than maxPluginBytes in all. Every
// write goes through an os.Root, so the kernel keeps it inside dir even if
// the name checks ever missed a case.
func untarPlugin(b []byte, dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	var total int64
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the plugin archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader { // git archive's commit id
			continue
		}
		name := path.Clean(h.Name)
		if !filepath.IsLocal(filepath.FromSlash(name)) || slices.Contains(strings.Split(h.Name, "/"), "..") {
			return fmt.Errorf("plugin entry %q escapes its folder", h.Name)
		}
		dst := filepath.FromSlash(name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxPluginBytes {
				return fmt.Errorf("the plugin holds more than %d bytes", maxPluginBytes)
			}
			if err := root.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("plugin entry %q has type %c; only files and folders allowed", h.Name, h.Typeflag)
		}
	}
}

// copyRegular copies a plain file into a new file.
func copyRegular(src, dst string, mode os.FileMode) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a plain file", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func writeNew(dst string, b []byte, mode os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, err = out.Write(b)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// treeSize adds up the sizes of the plain files under dir.
func treeSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		n += fi.Size()
		return nil
	})
	return n, err
}
