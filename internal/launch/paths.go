// Package launch holds cc-launch's logic: the root-owned helper that checks
// the sandbox, boots one Firecracker VM under the jailer and copies its
// results off the output disk. It takes nothing from its caller but a run id,
// and imports nothing that talks to the network.
package launch

import "path/filepath"

// Paths names every file cc-launch reads or writes. All of them sit in
// root-owned folders the setup steps create; none comes from the caller.
type Paths struct {
	StateDir    string // the images, their hashes and the run folders
	RunsDir     string
	ImagesSum   string // sha256sum lines for the files below
	Kernel      string
	RootDisk    string
	RulesHash   string // the expected nft rule hash, from setup
	BinDir      string
	Jailer      string
	Firecracker string
	JailerBase  string
	LockPath    string
	CgroupRoot  string
}

// DefaultPaths returns the footprint from spec §3.
func DefaultPaths() Paths {
	state := "/var/lib/codecheck-sandbox"
	bin := "/usr/local/lib/codecheck-sandbox/bin"
	return Paths{
		StateDir:    state,
		RunsDir:     filepath.Join(state, "runs"),
		ImagesSum:   filepath.Join(state, "images.sha256"),
		Kernel:      filepath.Join(state, "vmlinux"),
		RootDisk:    filepath.Join(state, "rootfs.ext4"),
		RulesHash:   filepath.Join(state, "rules.sha256"),
		BinDir:      bin,
		Jailer:      filepath.Join(bin, "jailer"),
		Firecracker: filepath.Join(bin, "firecracker"),
		JailerBase:  "/srv/jailer",
		LockPath:    "/run/codecheck-sandbox/lock",
		CgroupRoot:  "/sys/fs/cgroup",
	}
}
