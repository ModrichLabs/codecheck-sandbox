package step

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Uninstall runs Undo in reverse step order, but only for steps the manifest
// lists with at least one created item: adopted items (packages or users
// that existed before setup) and steps the manifest never mentions stay. A
// failed Undo does not stop the rest; Uninstall reports every failure.
func Uninstall(ctx context.Context, steps []Step, m *Manifest) error {
	created := map[string]bool{}
	for _, it := range m.Items {
		if it.State == Created {
			created[it.ID] = true
		}
	}
	var errs []error
	for _, s := range slices.Backward(steps) {
		if !created[s.ID()] {
			continue
		}
		if err := s.Undo(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.ID(), err))
		}
	}
	return errors.Join(errs...)
}
