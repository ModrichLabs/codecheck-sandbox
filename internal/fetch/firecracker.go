package fetch

import (
	"context"
	"os"
	"path"
	"path/filepath"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

// firecrackerMaxBytes caps each binary (3.9 MB and 2.2 MB at v1.18.0).
const firecrackerMaxBytes = 64 << 20

// FirecrackerRelease takes the firecracker and jailer binaries out of the
// pinned release tarball, checking the tarball and each binary.
type FirecrackerRelease struct {
	Downloader
	Pin     pins.FirecrackerPin
	WorkDir string // holds the tarball while it gets read
}

// Fetch writes both binaries (mode 0755), or neither.
func (r FirecrackerRelease) Fetch(ctx context.Context, firecrackerDest, jailerDest string) error {
	tgz := filepath.Join(r.WorkDir, path.Base(r.Pin.URL))
	if err := r.Download(ctx, r.Pin.URL, r.Pin.SHA256, tgz); err != nil {
		return err
	}
	defer os.Remove(tgz)
	dir := "release-" + r.Pin.Version + "-x86_64/"
	return extractMembers(tgz, []member{
		{name: dir + "firecracker-" + r.Pin.Version + "-x86_64", sha: r.Pin.FirecrackerSHA256, max: firecrackerMaxBytes, dest: firecrackerDest},
		{name: dir + "jailer-" + r.Pin.Version + "-x86_64", sha: r.Pin.JailerSHA256, max: firecrackerMaxBytes, dest: jailerDest},
	})
}
