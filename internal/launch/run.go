package launch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// lockWait bounds how long a run waits for another to finish: not at all,
// in effect, since one VM at a time means the caller should hear "busy".
const lockWait = 2 * time.Second

// pollEvery sets how often Wait reads the scope's cgroup.events.
const pollEvery = 250 * time.Millisecond

// Result describes a finished run.
type Result struct {
	Job    Job
	Copied []string // result files copied, for a review or selftest
}

// BootError wraps a failure to stage or start the VM.
type BootError struct{ Err error }

func (e *BootError) Error() string { return "starting the VM: " + e.Err.Error() }
func (e *BootError) Unwrap() error { return e.Err }

// ExtractError wraps a failure to copy results off the output disk.
type ExtractError struct{ Err error }

func (e *ExtractError) Error() string { return "copying results: " + e.Err.Error() }
func (e *ExtractError) Unwrap() error { return e.Err }

// Run does one whole run under the lock: check, stage, boot, wait, collect
// the writable disk, remove the jail, copy the results and tidy the run
// folder. An interrupted run still kills its VM and cleans up first.
func Run(ctx context.Context, e Env, m Mounter, id RunID, build bool) (Result, error) {
	lock, err := step.Lock(ctx, e.Paths.LockPath, lockWait)
	if err != nil {
		return Result{}, err
	}
	defer lock.Unlock()

	job, err := Verify(ctx, e, id, build)
	if err != nil {
		return Result{}, err
	}
	j, err := Stage(ctx, e, m, job)
	if err != nil {
		return Result{Job: job}, &BootError{err}
	}

	argv := j.Argv(e, job)
	job.Started = time.Now().UTC()
	res, err := e.Run.Run(ctx, system.Cmd{Name: argv[0], Args: argv[1:], CleanEnv: true, Stdout: e.Console, Stderr: e.Console})
	if err == nil && res.Code != 0 {
		detail := strings.TrimSpace(string(res.Stderr))
		if e.Console != nil {
			detail = "see the console output above"
		}
		err = fmt.Errorf("%s exit %d: %s", argv[0], res.Code, detail)
	}
	if err != nil && ctx.Err() == nil {
		// The scope may hold a half-started VM; take it down first, and leave
		// the jail in place when that fails.
		if serr := stop(e, j); serr != nil {
			return Result{Job: job}, &BootError{errors.Join(err, serr)}
		}
		return Result{Job: job}, &BootError{errors.Join(err, j.Cleanup(e, m))}
	}

	waitErr := j.Wait(ctx, e, Limit(job.Config.Mode), pollEvery)
	if ctx.Err() != nil {
		// Interrupted: kill the VM, wait for it on a fresh context, and leave
		// a jail that still holds anything in place.
		if err := stop(e, j); err != nil {
			return Result{Job: job}, errors.Join(ctx.Err(), err)
		}
		return Result{Job: job}, errors.Join(ctx.Err(), j.Cleanup(e, m))
	}
	if errors.Is(waitErr, ErrStuck) || (waitErr != nil && !errors.Is(waitErr, ErrTimeout)) {
		return Result{Job: job}, waitErr // the jail stays, for a person to inspect
	}

	if err := CollectOutDisk(e, j, job); err != nil {
		return Result{Job: job}, errors.Join(&ExtractError{err}, j.Cleanup(e, m))
	}
	if err := j.Cleanup(e, m); err != nil {
		return Result{Job: job}, err
	}
	copied, err := Extract(ctx, e, job)
	if err != nil {
		// A timed-out build lacks its marker: report the timeout as the cause.
		return Result{Job: job, Copied: copied}, errors.Join(waitErr, &ExtractError{err})
	}
	if err := Tidy(e, job); err != nil {
		return Result{Job: job, Copied: copied}, err
	}
	// A timed-out job still had its results copied; report the timeout too.
	return Result{Job: job, Copied: copied}, waitErr
}

// stop kills the VM's scope and waits, on a fresh context, for it to empty.
func stop(e Env, j *Jail) error {
	ctx, cancel := context.WithTimeout(context.Background(), killGrace+5*time.Second)
	defer cancel()
	if _, err := e.Run.Run(ctx, system.Cmd{Name: "systemctl", Args: []string{"kill", "--signal=SIGKILL", j.Unit}, CleanEnv: true}); err != nil {
		return err
	}
	if err := j.Wait(ctx, e, killGrace, pollEvery/5); err != nil && !errors.Is(err, ErrTimeout) {
		return err
	}
	return nil
}

// Clean removes a run folder under the lock, so it never pulls files from
// under a running VM.
func Clean(ctx context.Context, e Env, id RunID) error {
	lock, err := step.Lock(ctx, e.Paths.LockPath, lockWait)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return RemoveRun(e, id)
}
