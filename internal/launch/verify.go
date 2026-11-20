package launch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/rulehash"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// The proxy's listener and unit, matching hostnet.DefaultConfig (a hostnet
// test holds the two together, since cc-launch can't import hostnet).
const (
	ProxyAddr  = "172.16.0.1:3180"
	ProxyUnit  = "codecheck-proxy.service"
	RulesTable = "codecheck"
)

// The names of the checks, as CheckError reports them.
const (
	CheckBinaries = "binaries"
	CheckImages   = "image-hash"
	CheckRules    = "rule-hash"
	CheckProxy    = "proxy-owner"
	CheckRunDisks = "run-disks"
)

// CheckError reports which check refused the run.
type CheckError struct {
	Check string
	Err   error
}

func (e *CheckError) Error() string { return e.Check + ": " + e.Err.Error() }
func (e *CheckError) Unwrap() error { return e.Err }

func refuse(check, format string, args ...any) error {
	return &CheckError{Check: check, Err: fmt.Errorf(format, args...)}
}

// ProcFS reads /proc, so tests can describe processes and sockets.
type ProcFS interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]string, error)
	Readlink(path string) (string, error)
}

// RealProc reads the real /proc.
type RealProc struct{}

// ReadFile implements ProcFS.
func (RealProc) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// ReadDir implements ProcFS.
func (RealProc) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, err
}

// Readlink implements ProcFS.
func (RealProc) Readlink(path string) (string, error) { return os.Readlink(path) }

// Env holds what cc-launch works with.
type Env struct {
	Paths Paths
	Run   system.Runner // commands run as root already: never set Cmd.Root
	Proc  ProcFS
	Owner int // the uid every trusted file must belong to: 0 outside tests

	// JailUID and JailGID name the unprivileged account Firecracker runs
	// as (fcjail); main looks them up.
	JailUID, JailGID int

	// ResultUID and ResultGID own the copied results: the user who ran
	// sudo (SUDO_UID and SUDO_GID).
	ResultUID, ResultGID int
}

// Job describes a run that passed every check.
type Job struct {
	ID     RunID
	Config guestinit.Config
}

// Size caps for the run disks.
const (
	MaxCodeDisk   = 16 << 30
	MaxInputsDisk = 2 << 30
	MaxBaseDisk   = 2 << 30
	MaxKernel     = 128 << 20
	MaxInitrd     = 64 << 20
	minExt4       = 2048
)

// runFile describes one file a run folder must hold.
type runFile struct {
	name     string
	min, max int64
	magicAt  int64
	magic    []byte
}

var (
	ext4Magic     = []byte{0x53, 0xEF}
	jobFiles      = []runFile{{"code.ext4", minExt4, MaxCodeDisk, 1080, ext4Magic}}
	buildRunFiles = []runFile{
		{"vmlinux", 4, MaxKernel, 0, []byte("\x7fELF")},
		{"init.cpio", 6, MaxInitrd, 0, []byte("070701")},
		{"base.squashfs", 4, MaxBaseDisk, 0, []byte("hsqs")},
		{"inputs.ext4", minExt4, MaxInputsDisk, 1080, ext4Magic},
	}
)

// Verify runs every check before a boot, in order: the binaries, then (for
// reviews and selftests) the image hashes, the rule hash and the proxy's
// owner, then the run folder. The caller holds the lock already. build says
// whether the caller asked for a build; the config block's mode must agree.
func Verify(ctx context.Context, e Env, id RunID, build bool) (Job, error) {
	for _, b := range []string{e.Paths.Jailer, e.Paths.Firecracker} {
		if err := trusted(b, e.Owner, true); err != nil {
			return Job{}, refuse(CheckBinaries, "%w", err)
		}
	}
	if !build {
		if err := checkImages(e); err != nil {
			return Job{}, err
		}
		if err := checkRules(ctx, e); err != nil {
			return Job{}, err
		}
		if err := checkProxy(e.Proc); err != nil {
			return Job{}, err
		}
	}
	cfg, err := checkRunDir(e, id, build)
	if err != nil {
		return Job{}, err
	}
	return Job{ID: id, Config: cfg}, nil
}

// trusted refuses anything but a regular file owned by owner that no one
// else can write.
func trusted(path string, owner int, exec bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a plain file", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != owner {
		return fmt.Errorf("%s: not owned by uid %d", path, owner)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: writable by others (%v)", path, fi.Mode().Perm())
	}
	if exec && fi.Mode().Perm()&0o100 == 0 {
		return fmt.Errorf("%s: not executable", path)
	}
	return nil
}

// checkImages hashes the kernel and root disk against images.sha256, which
// must list exactly those two files.
func checkImages(e Env) error {
	if err := trusted(e.Paths.ImagesSum, e.Owner, false); err != nil {
		return refuse(CheckImages, "%w", err)
	}
	b, err := os.ReadFile(e.Paths.ImagesSum)
	if err != nil {
		return refuse(CheckImages, "%w", err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		sum, path, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			return refuse(CheckImages, "%s: malformed line %q", e.Paths.ImagesSum, line)
		}
		want[path] = sum
	}
	files := []string{e.Paths.Kernel, e.Paths.RootDisk}
	if len(want) != len(files) {
		return refuse(CheckImages, "%s lists %d files, want %d", e.Paths.ImagesSum, len(want), len(files))
	}
	for _, f := range files {
		sum, ok := want[f]
		if !ok {
			return refuse(CheckImages, "%s doesn't list %s", e.Paths.ImagesSum, f)
		}
		if err := trusted(f, e.Owner, false); err != nil {
			return refuse(CheckImages, "%w", err)
		}
		got, err := hashFile(f)
		if err != nil {
			return refuse(CheckImages, "%w", err)
		}
		if got != sum {
			return refuse(CheckImages, "%s: SHA-256 %s, want %s", f, got, sum)
		}
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkRules compares the loaded table with the hash setup recorded; a flush
// by nftables.service or an added rule both show up.
func checkRules(ctx context.Context, e Env) error {
	if err := trusted(e.Paths.RulesHash, e.Owner, false); err != nil {
		return refuse(CheckRules, "%w", err)
	}
	b, err := os.ReadFile(e.Paths.RulesHash)
	if err != nil {
		return refuse(CheckRules, "%w", err)
	}
	live, err := rulehash.Live(ctx, e.Run, RulesTable, false)
	if err != nil {
		return refuse(CheckRules, "%w", err)
	}
	if want := strings.TrimSpace(string(b)); live != want {
		return refuse(CheckRules, "the loaded rules hash to %s, want %s", live, want)
	}
	return nil
}

// checkProxy finds the one listener that covers the proxy's address and
// confirms the proxy unit's process holds it.
func checkProxy(p ProcFS) error {
	want := netip.MustParseAddrPort(ProxyAddr)
	var inodes []string
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := p.ReadFile(table)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && table == "/proc/net/tcp6" {
				continue // IPv6 off on the host
			}
			return refuse(CheckProxy, "%w", err)
		}
		found, err := listeners(b, want)
		if err != nil {
			return refuse(CheckProxy, "%s: %w", table, err)
		}
		inodes = append(inodes, found...)
	}
	if len(inodes) != 1 {
		return refuse(CheckProxy, "%d listeners cover %s, want exactly the proxy's", len(inodes), ProxyAddr)
	}
	pids, err := socketOwners(p, inodes[0])
	if err != nil {
		return refuse(CheckProxy, "%w", err)
	}
	if len(pids) == 0 {
		return refuse(CheckProxy, "no process holds the listener on %s", ProxyAddr)
	}
	for _, pid := range pids {
		b, err := p.ReadFile("/proc/" + pid + "/cgroup")
		if err != nil {
			return refuse(CheckProxy, "%w", err)
		}
		if !slices.Contains(strings.Split(strings.TrimSpace(string(b)), "\n"), "0::/system.slice/"+ProxyUnit) {
			return refuse(CheckProxy, "pid %s holds %s outside %s", pid, ProxyAddr, ProxyUnit)
		}
	}
	return nil
}

// listeners returns the socket inodes of LISTEN entries in a /proc/net/tcp
// table on want's port whose address covers want's: the address itself, or
// a wildcard.
func listeners(table []byte, want netip.AddrPort) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(table))
	sc.Scan() // the header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			return nil, fmt.Errorf("short line %q", sc.Text())
		}
		if f[3] != "0A" { // TCP_LISTEN
			continue
		}
		local, err := parseProcAddr(f[1])
		if err != nil {
			return nil, err
		}
		if local.Port() != want.Port() {
			continue
		}
		a := local.Addr().Unmap()
		if a == want.Addr() || a.IsUnspecified() {
			out = append(out, f[9])
		}
	}
	return out, sc.Err()
}

// parseProcAddr reads /proc/net/tcp's ADDR:PORT, where the address comes as
// 32-bit words in host (little-endian) order.
func parseProcAddr(s string) (netip.AddrPort, error) {
	addrHex, portHex, ok := strings.Cut(s, ":")
	port, err := strconv.ParseUint(portHex, 16, 16)
	raw, herr := hex.DecodeString(addrHex)
	if !ok || err != nil || herr != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, fmt.Errorf("address %q", s)
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	a, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(a, uint16(port)), nil
}

// socketOwners lists the pids with an open descriptor on the socket.
func socketOwners(p ProcFS, inode string) ([]string, error) {
	entries, err := p.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	target := "socket:[" + inode + "]"
	var pids []string
	for _, pid := range entries {
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		fds, err := p.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			continue // the process ended
		}
		for _, fd := range fds {
			if l, err := p.Readlink("/proc/" + pid + "/fd/" + fd); err == nil && l == target {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}

// checkRunDir checks the run folder: exactly the files the mode needs, each
// a trusted plain file within its size cap and with the right magic number,
// and a config block for this run in a mode that matches the request.
func checkRunDir(e Env, id RunID, build bool) (guestinit.Config, error) {
	dir := id.Dir(e.Paths)
	fi, err := os.Lstat(dir)
	if err != nil {
		return guestinit.Config{}, refuse(CheckRunDisks, "%w", err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || !ok || int(st.Uid) != e.Owner || fi.Mode().Perm()&0o022 != 0 {
		return guestinit.Config{}, refuse(CheckRunDisks, "%s: want a folder only its owner can write", dir)
	}

	cfgPath := filepath.Join(dir, "config.img")
	if err := trusted(cfgPath, e.Owner, false); err != nil {
		return guestinit.Config{}, refuse(CheckRunDisks, "%w", err)
	}
	block, err := os.ReadFile(cfgPath)
	if err != nil {
		return guestinit.Config{}, refuse(CheckRunDisks, "%w", err)
	}
	cfg, err := guestinit.Parse(block)
	if err != nil {
		return guestinit.Config{}, refuse(CheckRunDisks, "config.img: %w", err)
	}
	if cfg.RunID != id.String() {
		return guestinit.Config{}, refuse(CheckRunDisks, "config.img names run %s", cfg.RunID)
	}
	if (cfg.Mode == guestinit.ModeBuild) != build {
		return guestinit.Config{}, refuse(CheckRunDisks, "config.img asks for mode %s", cfg.Mode)
	}

	files := jobFiles
	if build {
		files = buildRunFiles
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return guestinit.Config{}, refuse(CheckRunDisks, "%w", err)
	}
	if len(names) != len(files)+1 {
		var got []string
		for _, n := range names {
			got = append(got, n.Name())
		}
		return guestinit.Config{}, refuse(CheckRunDisks, "%s holds %v, want config.img and %d disks", dir, got, len(files))
	}
	for _, f := range files {
		if err := checkRunFile(filepath.Join(dir, f.name), f, e.Owner); err != nil {
			return guestinit.Config{}, refuse(CheckRunDisks, "%w", err)
		}
	}
	return cfg, nil
}

func checkRunFile(path string, f runFile, owner int) error {
	if err := trusted(path, owner, false); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Size() < f.min || fi.Size() > f.max {
		return fmt.Errorf("%s: %d bytes, want %d to %d", path, fi.Size(), f.min, f.max)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	got := make([]byte, len(f.magic))
	if _, err := file.ReadAt(got, f.magicAt); err != nil || !bytes.Equal(got, f.magic) {
		return fmt.Errorf("%s: not the expected kind of image", path)
	}
	return nil
}
