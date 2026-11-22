// Command cc-launch boots one sandbox VM for codecheck-sandbox. The CLI runs
// it through `sudo -n`, so it runs as root, and it takes nothing that could
// change the sandbox: only a run id, whose folder the CLI filled beforehand.
//
//	cc-launch <run-id>         run the review or selftest the run's config names
//	cc-launch build <run-id>   run the guest image build
//	cc-launch clean <run-id>   remove the run folder once its results are read
//
// Exit codes: 0 done (the job's own status sits in result/exit-code); 1
// another failure; 2 usage; 3 another run holds the lock; 4 a check refused
// the run; 5 the VM failed to start; 6 the VM ran past its time limit; 7 the
// results could not be copied; 8 the environment is wrong (not root, no jail
// user); 130 interrupted.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// The documented exit codes live in the launch package, which the CLI's
// client shares.
const (
	ExitOK          = launch.ExitOK
	ExitFailure     = launch.ExitFailure
	ExitUsage       = launch.ExitUsage
	ExitBusy        = launch.ExitBusy
	ExitRefused     = launch.ExitRefused
	ExitBoot        = launch.ExitBoot
	ExitTimeout     = launch.ExitTimeout
	ExitResults     = launch.ExitResults
	ExitEnvironment = launch.ExitEnvironment
	ExitInterrupted = launch.ExitInterrupted
)

const usage = "usage: cc-launch <run-id> | cc-launch build <run-id> | cc-launch clean <run-id>"

// ops performs the actions, so tests can check the dispatch.
type ops interface {
	launch(ctx context.Context, id launch.RunID, build bool) error
	clean(ctx context.Context, id launch.RunID) error
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, os.Geteuid(), realOps{}))
}

// run parses the arguments, refuses anything but the three forms, does the
// action and maps the outcome to an exit code.
func run(args []string, stderr io.Writer, euid int, o ops) int {
	var action string
	var raw string
	switch {
	case len(args) == 1:
		action, raw = "launch", args[0]
	case len(args) == 2 && (args[0] == "build" || args[0] == "clean"):
		action, raw = args[0], args[1]
	default:
		fmt.Fprintln(stderr, usage)
		return ExitUsage
	}
	id, err := launch.ParseRunID(raw)
	if err != nil {
		fmt.Fprintf(stderr, "cc-launch: %v\n%s\n", err, usage)
		return ExitUsage
	}
	if euid != 0 {
		fmt.Fprintln(stderr, "cc-launch: runs only as root, through sudo")
		return ExitEnvironment
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	switch action {
	case "clean":
		err = o.clean(ctx, id)
	default:
		err = o.launch(ctx, id, action == "build")
	}
	if err != nil {
		fmt.Fprintf(stderr, "cc-launch: %v\n", err)
	}
	return exitCode(err)
}

func exitCode(err error) int {
	var check *launch.CheckError
	var boot *launch.BootError
	var extract *launch.ExtractError
	var env *envError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	case errors.Is(err, step.ErrBusy):
		return ExitBusy
	case errors.As(err, &check):
		return ExitRefused
	case errors.As(err, &boot):
		return ExitBoot
	case errors.Is(err, launch.ErrTimeout):
		return ExitTimeout
	case errors.As(err, &extract):
		return ExitResults
	case errors.As(err, &env):
		return ExitEnvironment
	}
	return ExitFailure
}

// envError reports a host that lacks what cc-launch needs.
type envError struct{ msg string }

func (e *envError) Error() string { return e.msg }

// realOps runs against the real host.
type realOps struct{}

func (realOps) env() (launch.Env, error) {
	jail, err := user.Lookup("fcjail")
	if err != nil {
		return launch.Env{}, &envError{"no fcjail account: run codecheck-sandbox setup"}
	}
	e := launch.Env{
		Paths:   launch.DefaultPaths(),
		Run:     system.ExecRunner{},
		Proc:    launch.RealProc{},
		Owner:   0,
		Console: os.Stderr,
	}
	e.JailUID, _ = strconv.Atoi(jail.Uid)
	e.JailGID, _ = strconv.Atoi(jail.Gid)
	// Results belong to whoever ran sudo; root itself when no one did.
	e.ResultUID, _ = strconv.Atoi(os.Getenv("SUDO_UID"))
	e.ResultGID, _ = strconv.Atoi(os.Getenv("SUDO_GID"))
	if e.JailUID == 0 {
		return launch.Env{}, &envError{"fcjail has uid 0; refusing to run the VM as root"}
	}
	return e, nil
}

func (o realOps) launch(ctx context.Context, id launch.RunID, build bool) error {
	e, err := o.env()
	if err != nil {
		return err
	}
	_, err = launch.Run(ctx, e, launch.RealMounter{}, id, build)
	return err
}

func (o realOps) clean(ctx context.Context, id launch.RunID) error {
	e, err := o.env()
	if err != nil {
		return err
	}
	return launch.Clean(ctx, e, id)
}
