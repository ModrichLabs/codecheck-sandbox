package repocheck

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
)

// bidiFiles lists the text files under root that hold a raw bidirectional
// control (U+202A–U+202E, U+2066–U+2069). It skips .git, worktrees and
// binary files (those with a NUL byte), and files over 4 MB.
func bidiFiles(root string) ([]string, error) {
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "worktrees" && filepath.Base(filepath.Dir(path)) == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 4<<20 {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return nil
		}
		for _, r := range string(b) {
			if r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
				bad = append(bad, path)
				break
			}
		}
		return nil
	})
	return bad, err
}
