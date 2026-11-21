package review

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// State names where a run has got to, for the live status.
type State string

// The states, in order.
const (
	StatePreparing  State = "preparing"  // cloning and packing disks
	StateBooting    State = "booting"    // cc-launch checks and boots the VM
	StateRunning    State = "running"    // the guest's job has started
	StateFinished   State = "finished"   // the job ended; the VM reboots
	StateCollecting State = "collecting" // reading the results
	StateDone       State = "done"
)

// Status reports a state change; the interface ticks elapsed time itself
// from Started.
type Status struct {
	State   State
	Started time.Time
}

// Launcher runs cc-launch (launch.Client).
type Launcher interface {
	Run(ctx context.Context, id launch.RunID) error
	Clean(ctx context.Context, id launch.RunID) error
}

// Pipeline runs reviews and selftests end to end.
type Pipeline struct {
	Run         system.Runner // the user's own commands (git, mkfs.ext4)
	Root        system.Runner // root commands, through the privilege tool
	Paths       launch.Paths
	WorkRoot    string // ~/codecheck-work
	ReportsRoot string // where report folders go
	Now         func() time.Time
	// Launcher makes the cc-launch client, given the console writer the
	// VM's serial log should stream to.
	Launcher func(console io.Writer) Launcher
	// Status hears each state change; nil drops them.
	Status func(Status)
	// Console also receives the raw serial log; nil drops it.
	Console io.Writer
}

// Options holds a review's settings.
type Options struct {
	Source    Source
	BudgetUSD string
}

// RunResult bundles what a run produced.
type RunResult struct {
	Summary   Summary
	ReportDir string
}

// Review prepares, runs and reports on one review.
func (p *Pipeline) Review(ctx context.Context, o Options) (RunResult, error) {
	started := p.Now()
	p.status(StatePreparing, started)
	id, err := launch.NewRunID(started)
	if err != nil {
		return RunResult{}, err
	}
	prep, err := Prepare(ctx, p.Run, p.WorkRoot, o.Source, started)
	if err != nil {
		return RunResult{}, err
	}
	defer os.RemoveAll(prep.Dir)
	cfg := guestinit.Config{Mode: guestinit.ModeReview, RunID: id.String(), MaxBudgetUSD: o.BudgetUSD,
		Scope: prep.Scope, Base: prep.Base}
	s := Summary{Target: o.Source.Target, Scope: string(prep.Scope), Base: prep.Base, Head: prep.Head}
	return p.run(ctx, id, prep.Dir, filepath.Join(prep.Dir, "code"), prep.Name, cfg, s, started)
}

// Selftest runs the escape probes; ports adds host ports to try, such as
// Docker's published ones.
func (p *Pipeline) Selftest(ctx context.Context, ports []int) (RunResult, error) {
	started := p.Now()
	p.status(StatePreparing, started)
	id, err := launch.NewRunID(started)
	if err != nil {
		return RunResult{}, err
	}
	if err := os.MkdirAll(p.WorkRoot, 0o700); err != nil {
		return RunResult{}, err
	}
	dir, err := os.MkdirTemp(p.WorkRoot, "selftest-")
	if err != nil {
		return RunResult{}, err
	}
	defer os.RemoveAll(dir)
	cfg := guestinit.Config{Mode: guestinit.ModeSelftest, RunID: id.String(), ProbePorts: ports}
	return p.run(ctx, id, dir, filepath.Join(dir, "code"), "selftest", cfg, Summary{}, started)
}

// run packs the disks, stages the run, boots it through cc-launch, then
// copies and analyses whatever came back, even after a failed run, and
// always cleans the run folder.
func (p *Pipeline) run(ctx context.Context, id launch.RunID, dir, codeDir, name string, cfg guestinit.Config, base Summary, started time.Time) (res RunResult, err error) {
	if err := MakeRunFiles(ctx, p.Run, dir, codeDir, cfg); err != nil {
		return RunResult{}, err
	}
	sw := newStatusWriter(id.String(), func(s State) { p.status(s, started) })
	console := io.Writer(sw)
	if p.Console != nil {
		console = io.MultiWriter(sw, p.Console)
	}
	l := p.Launcher(console)
	if err := launch.StageRun(ctx, p.Root, p.Paths, id, dir, RunFiles); err != nil {
		_ = l.Clean(context.WithoutCancel(ctx), id)
		return RunResult{}, err
	}
	defer func() {
		if cerr := l.Clean(context.WithoutCancel(ctx), id); cerr != nil && err == nil {
			err = cerr
		}
	}()

	p.status(StateBooting, started)
	runErr := l.Run(ctx, id)

	result := filepath.Join(id.Dir(p.Paths), "result")
	if _, err := os.Stat(result); err != nil {
		return RunResult{}, errors.Join(runErr, err)
	}
	p.status(StateCollecting, started)
	reportDir := filepath.Join(p.ReportsRoot, name+"-"+started.UTC().Format("20060102-150405"))
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		return RunResult{}, errors.Join(runErr, err)
	}
	for _, f := range []string{"stream.jsonl", "stderr.log", "proxy-denials.jsonl"} {
		if err := copyIfPresent(filepath.Join(result, f), filepath.Join(reportDir, f)); err != nil {
			return RunResult{}, errors.Join(runErr, err)
		}
	}
	s, report, err := Analyse(result, cfg.Mode)
	if err != nil {
		return RunResult{}, errors.Join(runErr, err)
	}
	s.RunID, s.Target, s.Scope, s.Base, s.Head = id.String(), base.Target, base.Scope, base.Base, base.Head
	s.Started, s.Finished = started.UTC(), p.Now().UTC()
	if err := WriteReport(reportDir, s, report); err != nil {
		return RunResult{}, errors.Join(runErr, err)
	}
	p.status(StateDone, started)
	return RunResult{Summary: s, ReportDir: reportDir}, runErr
}

func (p *Pipeline) status(s State, started time.Time) {
	if p.Status != nil {
		p.Status(Status{State: s, Started: started})
	}
}

// copyIfPresent copies a result file into the report folder at 0600.
func copyIfPresent(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	b, err := readCapped(src, maxStream)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// statusWriter watches the serial log for cc-guestinit's own lines about
// this run, and reports each state change once.
type statusWriter struct {
	mu      sync.Mutex
	runID   string
	partial []byte
	state   State
	report  func(State)
}

func newStatusWriter(runID string, report func(State)) *statusWriter {
	return &statusWriter{runID: runID, report: report}
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, b...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.line(strings.TrimSpace(string(w.partial[:i])))
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > 4096 { // a line that never ends carries no status
		w.partial = w.partial[:0]
	}
	return len(b), nil
}

func (w *statusWriter) line(l string) {
	rest, ok := strings.CutPrefix(l, "cc-guestinit: ")
	if !ok {
		return
	}
	f := strings.Fields(rest)
	switch {
	case len(f) == 3 && f[1] == w.runID && f[2] == "starting" && w.state == "":
		w.move(StateRunning)
	case len(f) >= 2 && f[1] == "finished:" && w.state == StateRunning:
		w.move(StateFinished)
	}
}

func (w *statusWriter) move(s State) {
	w.state = s
	w.report(s)
}
