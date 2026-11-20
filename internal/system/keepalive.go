package system

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeepAliveEvery sets how often StartKeepAlive refreshes the login. The
// shortest common sudo timeout runs 5 minutes, so 60 seconds leaves plenty of
// margin.
const KeepAliveEvery = 60 * time.Second

// ErrLoginExpired reports a privilege tool that would need a password again.
// The engine reacts by pausing and repeating the interactive handover.
var ErrLoginExpired = errors.New("privilege login expired")

// StartKeepAlive refreshes the privilege login every KeepAliveEvery until ctx
// ends. See KeepAlive.
func StartKeepAlive(ctx context.Context, r Runner, tool Tool) <-chan error {
	t := time.NewTicker(KeepAliveEvery)
	context.AfterFunc(ctx, t.Stop)
	return KeepAlive(ctx, r, tool, t.C)
}

// KeepAlive refreshes the privilege login on every value from tick:
// `-n -v` for sudo and sudo-rs, and `-n true` for doas, which can only check
// the login, not extend it. run0 asks polkit each time and keeps no login to
// refresh, so for run0 KeepAlive only waits for ctx.
//
// The returned channel carries the first failure, then closes; it also
// closes, empty, when ctx ends.
func KeepAlive(ctx context.Context, r Runner, tool Tool, tick <-chan time.Time) <-chan error {
	out := make(chan error, 1)
	var args []string
	switch tool.Kind {
	case ToolSudo, ToolSudoRS:
		args = []string{"-n", "-v"}
	case ToolDoas:
		args = []string{"-n", "true"}
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick:
				if args == nil {
					continue
				}
				err := refresh(ctx, r, Cmd{Name: tool.Path, Args: args})
				if ctx.Err() != nil {
					return // the caller stopped us; no failure to report
				}
				if err != nil {
					out <- err
					return
				}
			}
		}
	}()
	return out
}

func refresh(ctx context.Context, r Runner, c Cmd) error {
	res, err := r.Run(ctx, c)
	if err != nil {
		return fmt.Errorf("refreshing the privilege login: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("%w: %s", ErrLoginExpired, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
