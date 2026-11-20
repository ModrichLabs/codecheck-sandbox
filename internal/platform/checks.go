package platform

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

// Thresholds from spec §7. A machine with 8 GB fitted reports less than
// 8 GiB in MemTotal once firmware and the kernel take their share, so the RAM
// check accepts 7.5 GiB and up.
const (
	minMemTotalKB = 7864320 // 7.5 GiB
	minFreeBytes  = 10 << 30
	diskPath      = "/var/lib"
)

// ErrManual reports a check that setup cannot fix on its own.
var ErrManual = errors.New("fix manually")

var checkIDs = []string{
	"platform-arch", "platform-kvm", "platform-systemd", "platform-cgroup2",
	"platform-ram", "platform-disk", "platform-privilege",
	"platform-firewall", "platform-security-module",
}

func init() { telemetry.RegisterStepIDs(checkIDs...) }

// readOnly adapts a read-only test to step.Step: it plans nothing, and Apply
// only explains the manual fix.
type readOnly struct {
	id, title, fix string
	run            func(ctx context.Context) (step.Status, error)
}

func (c readOnly) ID() string                                     { return c.id }
func (c readOnly) Title() string                                  { return c.title }
func (c readOnly) Phase() step.Phase                              { return step.PhasePlatform }
func (c readOnly) Check(ctx context.Context) (step.Status, error) { return c.run(ctxOrBackground(ctx)) }
func (c readOnly) Plan(context.Context) (step.Plan, error)        { return step.Plan{}, nil }
func (c readOnly) Apply(context.Context) error                    { return fmt.Errorf("%w: %s", ErrManual, c.fix) }
func (c readOnly) Undo(context.Context, []step.Item) error        { return nil }

func ok(detail string) (step.Status, error)   { return step.Status{OK: true, Detail: detail}, nil }
func fail(detail string) (step.Status, error) { return step.Status{OK: false, Detail: detail}, nil }

// Checks returns the platform phase's steps, in the order the wizard shows
// them.
func Checks(env Env) []step.Step {
	return []step.Step{
		readOnly{"platform-arch", "CPU architecture", "run setup on an x86_64 machine", func(context.Context) (step.Status, error) {
			switch env.Arch {
			case "amd64":
				return ok("x86_64")
			case "arm64":
				return fail("aarch64 support has been deferred; this release needs x86_64")
			}
			return fail("unsupported architecture " + env.Arch + "; this release needs x86_64")
		}},
		readOnly{"platform-kvm", "KVM access", "enable virtualisation in the firmware, load the kvm module, and make /dev/kvm group kvm with mode 0660", func(context.Context) (step.Status, error) {
			return kvmCheck(env)
		}},
		readOnly{"platform-systemd", "systemd", "boot the host with systemd", func(context.Context) (step.Status, error) {
			info, err := env.FS.Stat("/run/systemd/system")
			if err != nil || !info.IsDir() {
				return fail("systemd does not run this host")
			}
			return ok("systemd running")
		}},
		readOnly{"platform-cgroup2", "cgroups v2", "boot with the unified cgroup hierarchy (systemd.unified_cgroup_hierarchy=1)", func(context.Context) (step.Status, error) {
			if _, err := env.FS.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
				return fail("no cgroups v2 unified hierarchy at /sys/fs/cgroup")
			}
			return ok("cgroups v2")
		}},
		readOnly{"platform-ram", "Memory", "use a machine with at least 8 GB of RAM", func(context.Context) (step.Status, error) {
			kb, err := memTotalKB(env.FS)
			if err != nil {
				return step.Status{}, err
			}
			detail := fmt.Sprintf("%.1f GiB", float64(kb)/(1<<20))
			if kb < minMemTotalKB {
				return fail(detail + "; setup needs 8 GB")
			}
			return ok(detail)
		}},
		readOnly{"platform-disk", "Free disk", "free at least 10 GB on the filesystem that holds " + diskPath, func(context.Context) (step.Status, error) {
			free, err := env.FreeBytes(diskPath)
			if err != nil {
				return step.Status{}, fmt.Errorf("checking free space on %s: %w", diskPath, err)
			}
			detail := fmt.Sprintf("%.1f GiB free on %s", float64(free)/(1<<30), diskPath)
			if free < minFreeBytes {
				return fail(detail + "; setup needs 10 GB")
			}
			return ok(detail)
		}},
		readOnly{"platform-privilege", "Privilege tool", "install sudo, sudo-rs, doas or run0", func(ctx context.Context) (step.Status, error) {
			tool, err := system.DetectTool(ctx, env.Runner, env.LookPath)
			if errors.Is(err, system.ErrNoPrivilegeTool) {
				return fail("no sudo, sudo-rs, doas or run0 on PATH")
			}
			if err != nil {
				return step.Status{}, err
			}
			return ok(string(tool.Kind))
		}},
		readOnly{"platform-firewall", "Firewall managers", "", func(context.Context) (step.Status, error) {
			r := Inspect(env)
			if len(r.FirewallsFound) == 0 {
				return ok("none found")
			}
			return ok("found " + strings.Join(r.FirewallsFound, ", ") + "; host setup adds allowances")
		}},
		readOnly{"platform-security-module", "Security module", "", func(context.Context) (step.Status, error) {
			r := Inspect(env)
			if r.SecurityModule == "none" {
				return ok("none")
			}
			return ok(string(r.SecurityModule) + " " + string(r.SecurityMode))
		}},
	}
}

// kvmCheck needs /dev/kvm as a character device that the jail user can open:
// readable and writable by everyone, or by group kvm, which host setup adds
// fcjail to.
func kvmCheck(env Env) (step.Status, error) {
	st, err := env.Stat("/dev/kvm")
	if errors.Is(err, fs.ErrNotExist) {
		return fail("no /dev/kvm: virtualisation looks disabled or the kvm module unloaded")
	}
	if err != nil {
		return step.Status{}, err
	}
	if st.Mode&fs.ModeCharDevice == 0 {
		return fail("/dev/kvm exists but holds no character device")
	}
	perm := st.Mode.Perm()
	if perm&0o006 == 0o006 {
		return ok("/dev/kvm open to all users")
	}
	group := groupName(env.FS, st.GID)
	if perm&0o060 == 0o060 && group == "kvm" {
		return ok("/dev/kvm group kvm, mode " + fmt.Sprintf("%04o", perm))
	}
	return fail(fmt.Sprintf("/dev/kvm has group %q and mode %04o; the jail user needs group kvm with read and write", group, perm))
}

// groupName looks gid up in /etc/group, returning its number when absent.
func groupName(fsys system.FS, gid uint32) string {
	data, err := fsys.ReadFile("/etc/group")
	if err == nil {
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			f := strings.Split(sc.Text(), ":")
			if len(f) >= 3 && f[2] == strconv.FormatUint(uint64(gid), 10) {
				return f[0]
			}
		}
	}
	return strconv.FormatUint(uint64(gid), 10)
}

func memTotalKB(fsys system.FS) (uint64, error) {
	data, err := fsys.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemTotal:" {
			return strconv.ParseUint(f[1], 10, 64)
		}
	}
	return 0, errors.New("/proc/meminfo: no MemTotal line")
}

// Report describes the host's firewall managers and security module, for
// later steps and the telemetry environment profile.
type Report struct {
	FirewallsFound []string
	Firewall       telemetry.FirewallManager
	SecurityModule telemetry.SecurityModule
	SecurityMode   telemetry.SecurityMode
}

// Inspect looks for firewalld and Docker (by their runtime files), an enabled
// ufw, and SELinux or AppArmor.
func Inspect(env Env) Report {
	exists := func(p string) bool { _, err := env.FS.Stat(p); return err == nil }
	var r Report
	if exists("/run/firewalld") {
		r.FirewallsFound = append(r.FirewallsFound, "firewalld")
	}
	if ufwEnabled(env.FS) {
		r.FirewallsFound = append(r.FirewallsFound, "ufw")
	}
	if exists("/run/docker.sock") || exists("/var/run/docker.sock") {
		r.FirewallsFound = append(r.FirewallsFound, "docker")
	}
	switch len(r.FirewallsFound) {
	case 0:
		r.Firewall = "none"
	case 1:
		r.Firewall = telemetry.FirewallManager(r.FirewallsFound[0])
	default:
		r.Firewall = "several"
	}

	r.SecurityModule, r.SecurityMode = "none", "disabled"
	if b, err := env.FS.ReadFile("/sys/fs/selinux/enforce"); err == nil {
		r.SecurityModule = "selinux"
		r.SecurityMode = "permissive"
		if strings.TrimSpace(string(b)) == "1" {
			r.SecurityMode = "enforcing"
		}
	} else if b, err := env.FS.ReadFile("/sys/module/apparmor/parameters/enabled"); err == nil && strings.TrimSpace(string(b)) == "Y" {
		r.SecurityModule, r.SecurityMode = "apparmor", "enforcing"
	}
	return r
}

func ufwEnabled(fsys system.FS) bool {
	data, err := fsys.ReadFile("/etc/ufw/ufw.conf")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(data)) {
		if strings.TrimSpace(line) == "ENABLED=yes" {
			return true
		}
	}
	return false
}
