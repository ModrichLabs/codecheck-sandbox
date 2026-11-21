package review

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// RunFiles lists what a review or selftest hands cc-launch; cc-launch makes
// the output disk itself.
var RunFiles = []string{"config.img", "code.ext4"}

// MakeRunFiles writes config.img and code.ext4 into dir. The code disk holds
// codeDir as its root (src/, and change/ for a diff review); a selftest gets
// an empty src/, so its read-only probe checks a real mount. No root needed.
func MakeRunFiles(ctx context.Context, r system.Runner, dir, codeDir string, cfg guestinit.Config) error {
	block, err := guestinit.Encode(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(codeDir, "src"), 0o755); err != nil {
		return err
	}
	files, err := treeBytes(codeDir)
	if err != nil {
		return err
	}
	size := codeDiskSize(files)
	if size > launch.MaxCodeDisk {
		return fmt.Errorf("the code takes %d MiB, past the %d MiB a code disk may hold", files>>20, launch.MaxCodeDisk>>20)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.img"), block, 0o644); err != nil {
		return err
	}
	disk := filepath.Join(dir, "code.ext4")
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
	res, err := r.Run(ctx, system.Cmd{Name: mkfs, Args: []string{"-q", "-F", "-L", "cc-code", "-d", codeDir, disk}, CleanEnv: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("mkfs.ext4: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// codeDiskSize allows the files plus 30% and 16 MiB for ext4's own
// metadata, in whole 4 KiB blocks.
func codeDiskSize(files int64) int64 {
	return (files*13/10 + 16<<20 + 4095) / 4096 * 4096
}

// treeBytes adds up the sizes of the plain files under dir.
func treeBytes(dir string) (int64, error) {
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
