package guestinit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// The drive layout, in the order vm.json attaches the drives. The config
// device always comes second, so the init can read it before it knows the
// mode.
const (
	ConfigDevice   = "/dev/vdb"
	reviewCodeDisk = "/dev/vdc"
	reviewOutDisk  = "/dev/vdd"
	selftestOut    = "/dev/vdc"
)

// The agent account the build creates, by number, so the init never parses
// /etc/passwd.
const (
	AgentUID  = 10001
	AgentHome = "/home/agent"
)

// Guest network settings, matching the host side (hostnet.DefaultConfig).
const (
	guestCIDR = "172.16.0.2/30"
	guestNIC  = "eth0"
)

// Paths inside the guest image.
const (
	ipPath       = "/usr/sbin/ip"
	setprivPath  = "/usr/bin/setpriv"
	ccClaudePath = "/usr/local/bin/cc-claude"
	selftestPath = "/usr/local/bin/cc-selftest"
	agentPath    = "/usr/local/bin:/usr/bin:/bin"
)

// MountKernelFS mounts /proc, /sys and /dev, which every mode needs before it
// can read the config device. The kernel may already have mounted devtmpfs
// on /dev; that counts as success.
func MountKernelFS(s Sys) error {
	for _, m := range []struct {
		fstype, target string
		flags          uintptr
	}{
		{"proc", "/proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"sysfs", "/sys", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"devtmpfs", "/dev", unix.MS_NOSUID | unix.MS_NOEXEC},
	} {
		if err := s.MkdirAll(m.target, 0o755); err != nil {
			return err
		}
		err := s.Mount(m.fstype, m.target, m.fstype, m.flags, "")
		if m.target == "/dev" && errors.Is(err, unix.EBUSY) {
			continue
		}
		if err != nil {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	return nil
}

// ReadConfig reads and parses the settings block from the config device.
func ReadConfig(s Sys) (Config, error) {
	b, err := s.ReadDevice(ConfigDevice, BlockSize)
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", ConfigDevice, err)
	}
	return Parse(b)
}

// RunJob runs a review or a selftest: it mounts the writable paths and the
// disks, brings up the guest's one network link, runs the job as the agent
// account and writes its exit code to the output disk. It returns the job's
// exit code; an error means the job never ran or its result could not be
// saved.
func RunJob(ctx context.Context, e Env, c Config) (int, error) {
	var program, outDisk string
	switch c.Mode {
	case ModeReview:
		program, outDisk = ccClaudePath, reviewOutDisk
	case ModeSelftest:
		program, outDisk = selftestPath, selftestOut
	default:
		return 0, fmt.Errorf("RunJob: mode %q runs no job", c.Mode)
	}
	if err := mountJobDisks(e.Sys, c.Mode, outDisk); err != nil {
		return 0, err
	}
	if err := setUpNetwork(ctx, e); err != nil {
		return 0, err
	}

	stdout, err := e.Sys.Create("/out/stream.jsonl", 0o600)
	if err != nil {
		return 0, err
	}
	defer stdout.Close()
	stderr, err := e.Sys.Create("/out/stderr.log", 0o600)
	if err != nil {
		return 0, err
	}
	defer stderr.Close()

	res, err := e.Run.Run(ctx, system.Cmd{
		Name: setprivPath,
		Args: []string{
			"--reuid=" + strconv.Itoa(AgentUID), "--regid=" + strconv.Itoa(AgentUID),
			"--clear-groups", "--no-new-privs", "--inh-caps=-all", "--bounding-set=-all",
			"--", program,
		},
		Env:      jobEnv(c),
		CleanEnv: true,
		Dir:      AgentHome,
		Stdout:   stdout, Stderr: stderr,
	})
	if err != nil {
		return 0, fmt.Errorf("running %s: %w", program, err)
	}
	if err := stdout.Close(); err != nil {
		return 0, err
	}
	if err := stderr.Close(); err != nil {
		return 0, err
	}
	if err := e.Sys.WriteFile("/out/exit-code", []byte(strconv.Itoa(res.Code)+"\n"), 0o600); err != nil {
		return 0, err
	}
	e.Sys.Sync()
	return res.Code, nil
}

// mountJobDisks puts a tmpfs over every path the job may write, then mounts
// the code disk (reviews only) and the output disk.
func mountJobDisks(s Sys, mode Mode, outDisk string) error {
	type mnt struct {
		source, target, fstype string
		flags                  uintptr
		data                   string
	}
	const tmp = unix.MS_NOSUID | unix.MS_NODEV
	const disk = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
	agent := strconv.Itoa(AgentUID)
	mounts := []mnt{
		{"tmpfs", "/tmp", "tmpfs", tmp, "mode=1777"},
		{"tmpfs", "/run", "tmpfs", tmp, "mode=0755"},
		{"tmpfs", "/var/tmp", "tmpfs", tmp, "mode=1777"},
		{"tmpfs", AgentHome, "tmpfs", tmp, "mode=0700,uid=" + agent + ",gid=" + agent},
	}
	if mode == ModeReview {
		// noload skips journal replay, so a crafted journal can't write to
		// the read-only disk.
		mounts = append(mounts, mnt{reviewCodeDisk, "/work", "ext4", disk | unix.MS_RDONLY, "noload"})
	}
	mounts = append(mounts, mnt{outDisk, "/out", "ext4", disk, ""})
	for _, m := range mounts {
		if err := s.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	return nil
}

// setUpNetwork turns IPv6 off, then gives eth0 the guest address with no
// default route: the proxy sits on the same /30, and nothing else should
// answer.
func setUpNetwork(ctx context.Context, e Env) error {
	for _, p := range []string{"all", "default"} {
		if err := e.Sys.WriteFile("/proc/sys/net/ipv6/conf/"+p+"/disable_ipv6", []byte("1\n"), 0o644); err != nil {
			return fmt.Errorf("turning IPv6 off: %w", err)
		}
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"addr", "add", guestCIDR, "dev", guestNIC},
		{"link", "set", guestNIC, "up"},
	} {
		res, err := e.Run.Run(ctx, system.Cmd{Name: ipPath, Args: args, CleanEnv: true})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("ip %s: exit %d: %s", strings.Join(args, " "), res.Code, strings.TrimSpace(string(res.Stderr)))
		}
	}
	return nil
}

// jobEnv hands the run settings to the job through its environment, so the
// agent never needs to read the root-only config device.
func jobEnv(c Config) []string {
	env := []string{"PATH=" + agentPath, "HOME=" + AgentHome, "CC_RUN_ID=" + c.RunID}
	switch c.Mode {
	case ModeReview:
		env = append(env, "CC_MAX_BUDGET_USD="+c.MaxBudgetUSD, "CC_SCOPE="+string(c.Scope))
		if c.Base != "" {
			env = append(env, "CC_BASE="+c.Base)
		}
	case ModeSelftest:
		if len(c.ProbePorts) > 0 {
			s := make([]string, len(c.ProbePorts))
			for i, p := range c.ProbePorts {
				s[i] = strconv.Itoa(p)
			}
			env = append(env, "CC_PROBE_PORTS="+strings.Join(s, ","))
		}
	}
	return env
}
