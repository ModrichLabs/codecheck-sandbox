package launch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/rulehash"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeProc stands in for /proc.
type fakeProc struct {
	files map[string]string
	dirs  map[string][]string
	links map[string]string
}

func (p *fakeProc) ReadFile(path string) ([]byte, error) {
	if s, ok := p.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func (p *fakeProc) ReadDir(path string) ([]string, error) {
	if d, ok := p.dirs[path]; ok {
		return d, nil
	}
	return nil, fs.ErrNotExist
}

func (p *fakeProc) Readlink(path string) (string, error) {
	if l, ok := p.links[path]; ok {
		return l, nil
	}
	return "", fs.ErrNotExist
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// proxyListening describes the proxy holding 172.16.0.1:3180 as pid 412.
func proxyListening() *fakeProc {
	return &fakeProc{
		files: map[string]string{
			"/proc/net/tcp": tcpHeader +
				"   0: 0100007F:0277 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1111 1 0000000000000000 100 0 0 10 0\n" +
				"   1: 010010AC:0C6C 00000000:0000 0A 00000000:00000000 00:00000000 00000000 61234        0 98765 1 0000000000000000 100 0 0 10 0\n",
			"/proc/net/tcp6":   tcpHeader,
			"/proc/412/cgroup": "0::/system.slice/codecheck-proxy.service\n",
			"/proc/77/cgroup":  "0::/user.slice/user-1000.slice/session-2.scope\n",
		},
		dirs: map[string][]string{
			"/proc":        {"1", "77", "412", "self", "net", "sys"},
			"/proc/1/fd":   {"0"},
			"/proc/77/fd":  {"0", "1", "5"},
			"/proc/412/fd": {"0", "3", "7"},
		},
		links: map[string]string{
			"/proc/1/fd/0":   "/dev/null",
			"/proc/77/fd/5":  "socket:[1111]",
			"/proc/412/fd/3": "anon_inode:[eventpoll]",
			"/proc/412/fd/7": "socket:[98765]",
		},
	}
}

type rig struct {
	env  Env
	run  *system.FakeRunner
	proc *fakeProc
	id   RunID
	dir  string
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func write(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// ext4Image returns n bytes with the ext4 superblock magic in place.
func ext4Image(n int) []byte {
	b := make([]byte, n)
	b[1080], b[1081] = 0x53, 0xEF
	return b
}

func newRig(t *testing.T, cfg guestinit.Config) *rig {
	t.Helper()
	root := t.TempDir()
	p := DefaultPaths()
	for _, f := range []*string{&p.StateDir, &p.RunsDir, &p.ImagesSum, &p.Kernel, &p.RootDisk, &p.RulesHash,
		&p.BinDir, &p.Jailer, &p.Firecracker, &p.JailerBase, &p.LockPath, &p.CgroupRoot} {
		*f = filepath.Join(root, *f)
	}
	kernel, rootfs := []byte("ELF kernel"), ext4Image(4096)
	write(t, p.Kernel, kernel, 0o644)
	write(t, p.RootDisk, rootfs, 0o644)
	write(t, p.ImagesSum, []byte(fmt.Sprintf("%s  %s\n%s  %s\n", sha(kernel), p.Kernel, sha(rootfs), p.RootDisk)), 0o644)
	write(t, p.Jailer, []byte("jailer"), 0o755)
	write(t, p.Firecracker, []byte("firecracker"), 0o755)

	nft, err := os.ReadFile("../rulehash/testdata/nft-list.json")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := rulehash.Hash(nft)
	write(t, p.RulesHash, []byte(h+"\n"), 0o644)
	run := &system.FakeRunner{}
	run.On("nft", "-j", "list", "table", "inet", "codecheck").Return(system.Result{Stdout: nft})

	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	dir := id.Dir(p)
	block, err := guestinit.Encode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "config.img"), block, 0o644)
	if cfg.Mode == guestinit.ModeBuild {
		write(t, filepath.Join(dir, "vmlinux"), []byte("\x7fELF build kernel"), 0o644)
		write(t, filepath.Join(dir, "init.cpio"), []byte("070701 cpio"), 0o644)
		write(t, filepath.Join(dir, "base.squashfs"), []byte("hsqs squashfs"), 0o644)
		write(t, filepath.Join(dir, "inputs.ext4"), ext4Image(8192), 0o644)
	} else {
		write(t, filepath.Join(dir, "code.ext4"), ext4Image(8192), 0o644)
	}

	proc := proxyListening()
	return &rig{
		env:  Env{Paths: p, Run: run, Proc: proc, Owner: os.Getuid()},
		run:  run,
		proc: proc,
		id:   id,
		dir:  dir,
	}
}

func reviewCfg() guestinit.Config {
	return guestinit.Config{Mode: guestinit.ModeReview, RunID: "cc-20261121-101500-a1b2c3", MaxBudgetUSD: "5", Scope: guestinit.ScopeFull}
}

func checkFails(t *testing.T, r *rig, build bool, check string) {
	t.Helper()
	_, err := Verify(context.Background(), r.env, r.id, build)
	var ce *CheckError
	if !errors.As(err, &ce) || ce.Check != check {
		t.Fatalf("Verify = %v, want a %s failure", err, check)
	}
}

func TestVerifyPassesASoundSandbox(t *testing.T) {
	r := newRig(t, reviewCfg())
	job, err := Verify(context.Background(), r.env, r.id, false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if job.Config.Mode != guestinit.ModeReview || job.ID != r.id {
		t.Errorf("job = %+v", job)
	}
	if c := r.run.Calls(); len(c) != 1 || c[0].Root {
		t.Errorf("nft calls = %+v; want one, without the privilege tool", c)
	}
}

func TestVerifyBinaries(t *testing.T) {
	r := newRig(t, reviewCfg())
	if err := os.Chmod(r.env.Paths.Jailer, 0o777); err != nil {
		t.Fatal(err)
	}
	checkFails(t, r, false, CheckBinaries)

	r = newRig(t, reviewCfg())
	r.env.Owner = os.Getuid() + 1 // someone else owns the files
	checkFails(t, r, false, CheckBinaries)

	r = newRig(t, reviewCfg())
	os.Remove(r.env.Paths.Firecracker)
	if err := os.Symlink("/bin/true", r.env.Paths.Firecracker); err != nil {
		t.Fatal(err)
	}
	checkFails(t, r, false, CheckBinaries)
}

func TestVerifyImageHashes(t *testing.T) {
	for name, mutate := range map[string]func(r *rig){
		"changed root disk": func(r *rig) { write(t, r.env.Paths.RootDisk, ext4Image(8192), 0o644) },
		"missing kernel":    func(r *rig) { os.Remove(r.env.Paths.Kernel) },
		"no hash file":      func(r *rig) { os.Remove(r.env.Paths.ImagesSum) },
		"kernel line gone": func(r *rig) {
			b, _ := os.ReadFile(r.env.Paths.ImagesSum)
			write(t, r.env.Paths.ImagesSum, []byte(strings.SplitAfter(string(b), "\n")[1]), 0o644)
		},
		"extra line": func(r *rig) {
			b, _ := os.ReadFile(r.env.Paths.ImagesSum)
			write(t, r.env.Paths.ImagesSum, append(b, []byte(strings.Repeat("0", 64)+"  /etc/passwd\n")...), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, reviewCfg())
			mutate(r)
			checkFails(t, r, false, CheckImages)
		})
	}
}

func TestVerifyRuleHash(t *testing.T) {
	r := newRig(t, reviewCfg())
	write(t, r.env.Paths.RulesHash, []byte(strings.Repeat("0", 64)+"\n"), 0o644)
	checkFails(t, r, false, CheckRules)

	r = newRig(t, reviewCfg())
	r.run = &system.FakeRunner{}
	r.run.On("nft").Return(system.Result{Code: 1, Stderr: []byte("Error: No such file or directory")})
	r.env.Run = r.run
	checkFails(t, r, false, CheckRules)
}

func TestVerifyProxyOwner(t *testing.T) {
	for name, mutate := range map[string]func(p *fakeProc){
		"nothing listening": func(p *fakeProc) { p.files["/proc/net/tcp"] = tcpHeader },
		"another unit": func(p *fakeProc) {
			p.files["/proc/412/cgroup"] = "0::/system.slice/evil.service\n"
		},
		"a look-alike unit": func(p *fakeProc) {
			p.files["/proc/412/cgroup"] = "0::/system.slice/codecheck-proxy.service.d/x\n"
		},
		"no process holds the socket": func(p *fakeProc) { delete(p.links, "/proc/412/fd/7") },
		"a wildcard listener as well": func(p *fakeProc) {
			p.files["/proc/net/tcp"] += "   2: 00000000:0C6C 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 55555 1 0000000000000000 100 0 0 10 0\n"
		},
		"an IPv6 wildcard listener": func(p *fakeProc) {
			p.files["/proc/net/tcp6"] = tcpHeader +
				"   0: 00000000000000000000000000000000:0C6C 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 55556 1 0000000000000000 100 0 0 10 0\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, reviewCfg())
			mutate(r.proc)
			checkFails(t, r, false, CheckProxy)
		})
	}
}

func TestVerifyProxyIgnoresConnectionsAndOtherPorts(t *testing.T) {
	r := newRig(t, reviewCfg())
	// An established connection to the proxy (state 01) is no listener.
	r.proc.files["/proc/net/tcp"] += "   2: 010010AC:0C6C 020010AC:D431 01 00000000:00000000 00:00000000 00000000 61234        0 44444 1 0000000000000000 100 0 0 10 0\n"
	if _, err := Verify(context.Background(), r.env, r.id, false); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyRunDisks(t *testing.T) {
	for name, mutate := range map[string]func(r *rig){
		"no config":      func(r *rig) { os.Remove(filepath.Join(r.dir, "config.img")) },
		"short config":   func(r *rig) { write(t, filepath.Join(r.dir, "config.img"), []byte("mode=review\n"), 0o644) },
		"garbage config": func(r *rig) { write(t, filepath.Join(r.dir, "config.img"), make([]byte, 4096), 0o644) },
		"no code disk":   func(r *rig) { os.Remove(filepath.Join(r.dir, "code.ext4")) },
		"not ext4":       func(r *rig) { write(t, filepath.Join(r.dir, "code.ext4"), make([]byte, 8192), 0o644) },
		"tiny code disk": func(r *rig) { write(t, filepath.Join(r.dir, "code.ext4"), []byte{1, 2}, 0o644) },
		"world-writable": func(r *rig) { os.Chmod(filepath.Join(r.dir, "code.ext4"), 0o666) },
		"symlinked disk": func(r *rig) {
			p := filepath.Join(r.dir, "code.ext4")
			os.Remove(p)
			os.Symlink(r.env.Paths.RootDisk, p)
		},
		"build config": func(r *rig) {
			b, _ := guestinit.Encode(guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"})
			write(t, filepath.Join(r.dir, "config.img"), b, 0o644)
		},
		"another run's config": func(r *rig) {
			c := reviewCfg()
			c.RunID = "cc-20261121-101500-zzzzzz"
			b, _ := guestinit.Encode(c)
			write(t, filepath.Join(r.dir, "config.img"), b, 0o644)
		},
		"stray file": func(r *rig) { write(t, filepath.Join(r.dir, "vm.json"), []byte("{}"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, reviewCfg())
			mutate(r)
			checkFails(t, r, false, CheckRunDisks)
		})
	}
}

func TestVerifyCapsTheCodeDisk(t *testing.T) {
	r := newRig(t, reviewCfg())
	f, err := os.OpenFile(filepath.Join(r.dir, "code.ext4"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxCodeDisk + 1); err != nil { // sparse, so cheap
		t.Fatal(err)
	}
	f.Close()
	checkFails(t, r, false, CheckRunDisks)
}

func TestVerifyBuild(t *testing.T) {
	r := newRig(t, guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"})
	// No image yet, no network device: the build checks binaries and its disks.
	os.Remove(r.env.Paths.ImagesSum)
	r.proc.files["/proc/net/tcp"] = tcpHeader
	job, err := Verify(context.Background(), r.env, r.id, true)
	if err != nil {
		t.Fatalf("Verify build: %v", err)
	}
	if job.Config.Mode != guestinit.ModeBuild || len(r.run.Calls()) != 0 {
		t.Errorf("job %+v, nft calls %d", job, len(r.run.Calls()))
	}

	write(t, filepath.Join(r.dir, "base.squashfs"), []byte("not squashfs"), 0o644)
	checkFails(t, r, true, CheckRunDisks)
}

func TestVerifyRefusesAReviewThroughBuild(t *testing.T) {
	r := newRig(t, reviewCfg())
	checkFails(t, r, true, CheckRunDisks)
}

func TestVerifySelftestNeedsACodeDisk(t *testing.T) {
	r := newRig(t, guestinit.Config{Mode: guestinit.ModeSelftest, RunID: "cc-20261121-101500-a1b2c3"})
	if _, err := Verify(context.Background(), r.env, r.id, false); err != nil {
		t.Fatalf("Verify selftest: %v", err)
	}
	nft, _ := os.ReadFile("../rulehash/testdata/nft-list.json")
	r.run.On("nft").Return(system.Result{Stdout: nft}) // for the second Verify
	os.Remove(filepath.Join(r.dir, "code.ext4"))
	checkFails(t, r, false, CheckRunDisks)
}
