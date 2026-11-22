package system

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// sbinDirs hold system tools an ordinary user's PATH leaves out on some
// distributions (Debian among them).
var sbinDirs = []string{"/usr/sbin", "/sbin"}

// LookTool finds a system tool, such as mkfs.ext4 or debugfs, on PATH or in
// the sbin folders.
func LookTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range sbinDirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found: install e2fsprogs", name)
}
