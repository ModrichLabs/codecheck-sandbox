// Package step defines the unit of setup work and the engine that runs it.
// Each Step knows how to check, plan, apply and undo one piece of the host
// footprint; setup, doctor, --dry-run and uninstall all drive the same steps.
package step

import (
	"context"
	"io/fs"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Phase groups steps the way the setup wizard shows them.
type Phase string

const (
	PhasePlatform   Phase = "platform"
	PhaseComponents Phase = "components"
	PhaseHost       Phase = "host"
	PhaseGuest      Phase = "guest"
	PhaseVerify     Phase = "verify"
)

// Step describes one idempotent piece of setup.
type Step interface {
	ID() string
	Title() string
	Phase() Phase
	// Check reports whether the machine already holds this step's result. An
	// error means the check itself could not run.
	Check(ctx context.Context) (Status, error)
	// Plan lists exactly what Apply will run and write, for the confirm panel
	// and --dry-run.
	Plan(ctx context.Context) (Plan, error)
	Apply(ctx context.Context) error
	// Undo removes exactly the given items: the ones the manifest records as
	// created by this step. It must leave everything else alone, including
	// items the same step adopted.
	Undo(ctx context.Context, items []Item) error
}

// Status holds a Check result. Detail explains it in a line for doctor.
type Status struct {
	OK     bool
	Detail string
}

// PlannedCmd names one command Apply will run.
type PlannedCmd = system.Cmd

// PlannedFile names one file Apply will write, with the exact bytes.
type PlannedFile struct {
	Path    string
	Mode    fs.FileMode
	Content []byte
}

// Plan lists what a step's Apply will do.
type Plan struct {
	Cmds  []PlannedCmd
	Files []PlannedFile
	// NeedsRoot makes the engine hand over for the privilege login first.
	NeedsRoot bool
}
