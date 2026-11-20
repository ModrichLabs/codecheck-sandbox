// Command cc-guestinit runs as PID 1 inside the sandbox's Firecracker guests.
// It reads its mode from the config device, builds the guest image, runs a
// review or runs the escape probes, then reboots, which ends the VM. Build it
// with CGO_ENABLED=0: the builder boots it from an initramfs with nothing
// else beside it.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func main() {
	os.Exit(run(os.Getpid(), os.Stderr, func() {
		e := guestinit.Env{Sys: guestinit.RealSys{}, Run: system.ExecRunner{}}
		guestinit.RunInit(context.Background(), e, guestinit.RealOps{}, os.Stderr, guestinit.Reboot)
	}))
}

// run refuses to start anywhere but PID 1, so running the binary by mistake
// on a host can never reboot it.
func run(pid int, console io.Writer, start func()) int {
	if pid != 1 {
		fmt.Fprintln(console, "cc-guestinit: runs only as PID 1 inside a sandbox VM")
		return 2
	}
	start()
	return 0
}
