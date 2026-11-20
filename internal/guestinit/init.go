package guestinit

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

// RunInit does the whole of the guest's PID 1: mount the kernel
// filesystems, read the config device, run the mode's job, then sync and
// reboot. It always ends in reboot, even after an error or a panic, since
// Firecracker exits on a guest reboot but hangs on a power-off (spike S1).
func RunInit(ctx context.Context, e Env, ops FileOps, console io.Writer, reboot func() error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(console, "cc-guestinit: panic: %v\n", r)
		}
		e.Sys.Sync()
		if err := reboot(); err != nil {
			// Returning from PID 1 panics the kernel, and panic=1 then
			// reboots it anyway.
			fmt.Fprintf(console, "cc-guestinit: reboot: %v\n", err)
		}
	}()
	if err := run(ctx, e, ops, console); err != nil {
		fmt.Fprintf(console, "cc-guestinit: %v\n", err)
	}
}

func run(ctx context.Context, e Env, ops FileOps, console io.Writer) error {
	if err := MountKernelFS(e.Sys); err != nil {
		return err
	}
	c, err := ReadConfig(e.Sys)
	if err != nil {
		return err
	}
	fmt.Fprintf(console, "cc-guestinit: %s %s starting\n", c.Mode, c.RunID)
	switch c.Mode {
	case ModeBuild:
		if err := Build(ctx, e, ops, c, DefaultBuildPaths); err != nil {
			return fmt.Errorf("build: %w", err)
		}
		fmt.Fprintln(console, "cc-guestinit: build finished")
	default:
		code, err := RunJob(ctx, e, c)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Mode, err)
		}
		fmt.Fprintf(console, "cc-guestinit: %s finished: exit %d\n", c.Mode, code)
	}
	return nil
}

// Reboot asks the kernel to restart, which makes Firecracker exit cleanly.
func Reboot() error { return unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART) }
