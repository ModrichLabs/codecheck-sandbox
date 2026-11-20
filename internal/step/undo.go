package step

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Uninstall runs Undo in reverse step order, handing each step only the items
// the manifest records as created under its ID: adopted items (packages or
// users that existed before setup) and steps the manifest never mentions
// stay. It elevates once before the first Undo and again if the login
// expires; a dry run reports what it would undo and changes nothing.
//
// A failed Undo does not stop the rest; Uninstall returns every failure. A
// nil manifest gives ErrNoManifest, so the caller can fall back to the
// footprint scan.
func Uninstall(ctx context.Context, steps []Step, m *Manifest, opts Options) error {
	if m == nil {
		return ErrNoManifest
	}
	created := map[string][]Item{}
	for _, it := range m.Items {
		if it.State == Created {
			created[it.ID] = append(created[it.ID], it)
		}
	}
	e := &engine{opts: opts}
	var errs []error
	for _, s := range slices.Backward(steps) {
		items := created[s.ID()]
		if len(items) == 0 {
			continue
		}
		base := Event{StepID: s.ID(), Title: s.Title()}
		e.emit(ctx, with(base, StepStarted))
		if opts.DryRun {
			e.emit(ctx, with(base, StepPlanned))
			continue
		}
		if !e.elevated {
			if err := e.elevate(ctx, base); err != nil {
				return errors.Join(append(errs, err)...)
			}
		}
		err := e.withLogin(ctx, base, func() error { return s.Undo(ctx, items) })
		if err != nil {
			ev := with(base, StepFailed)
			ev.Err = err
			e.emit(ctx, ev)
			errs = append(errs, fmt.Errorf("%s: %w", s.ID(), err))
			continue
		}
		e.emit(ctx, with(base, StepDone))
	}
	return errors.Join(errs...)
}

// with copies base with its Kind set.
func with(base Event, k EventKind) Event {
	base.Kind = k
	return base
}
