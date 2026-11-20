package step

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func undoManifest(items ...Item) *Manifest {
	return &Manifest{Version: ManifestVersion, Items: items}
}

func uninstall(t *testing.T, h *harness, steps []Step, m *Manifest, opts Options) error {
	t.Helper()
	if opts.Elevate == nil {
		opts.Elevate = func(context.Context) error {
			h.log = append(h.log, "elevate")
			return nil
		}
	}
	return Uninstall(context.Background(), steps, m, opts)
}

func TestUninstallUndoesCreatedStepsInReverse(t *testing.T) {
	h := &harness{}
	steps := []Step{h.step("user", true), h.step("packages", true), h.step("rules", true), h.step("proxy", true), h.step("forgotten", true)}
	m := undoManifest(
		Item{ID: "user", Kind: KindUser, Name: "fcjail", State: Created},
		Item{ID: "packages", Kind: KindPackage, Name: "nftables", State: Adopted},
		Item{ID: "rules", Kind: KindFile, Path: "/etc/codecheck-sandbox/rules.nft", State: Created},
		Item{ID: "proxy", Kind: KindUnit, Name: "codecheck-proxy.service", State: Created},
		Item{ID: "stale", Kind: KindUnit, Name: "codecheck-net.service", State: Created}, // no such step
	)
	if err := uninstall(t, h, steps, m, Options{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	want := []string{"elevate", "proxy.undo", "rules.undo", "user.undo"}
	if !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
}

func TestUninstallPassesOnlyCreatedItems(t *testing.T) {
	h := &harness{}
	s := h.step("packages", true)
	m := undoManifest(
		Item{ID: "packages", Kind: KindPackage, Name: "nftables", State: Adopted},
		Item{ID: "packages", Kind: KindPackage, Name: "squashfs-tools", State: Created},
	)
	if err := uninstall(t, h, []Step{s}, m, Options{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	want := []Item{{ID: "packages", Kind: KindPackage, Name: "squashfs-tools", State: Created}}
	if !reflect.DeepEqual(s.undone, want) {
		t.Errorf("Undo got %+v, want only the created item %+v", s.undone, want)
	}
}

func TestUninstallKeepsGoingAfterAFailure(t *testing.T) {
	h := &harness{}
	boom := errors.New("boom")
	a, b, c := h.step("a", true), h.step("b", true), h.step("c", true)
	b.undoErr = []error{boom}
	m := undoManifest(
		Item{ID: "a", Kind: KindUser, Name: "fcjail", State: Created},
		Item{ID: "b", Kind: KindUnit, Name: "codecheck-net.service", State: Created},
		Item{ID: "c", Kind: KindUnit, Name: "codecheck-proxy.service", State: Created},
	)
	err := uninstall(t, h, []Step{a, b, c}, m, Options{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if want := []string{"elevate", "c.undo", "b.undo", "a.undo"}; !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
}

func TestUninstallReElevatesWhenTheLoginExpires(t *testing.T) {
	h := &harness{}
	s := h.step("user", true)
	s.undoErr = []error{fmt.Errorf("userdel: %w", system.ErrLoginExpired)}
	m := undoManifest(Item{ID: "user", Kind: KindUser, Name: "fcjail", State: Created})
	if err := uninstall(t, h, []Step{s}, m, Options{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if want := []string{"elevate", "user.undo", "elevate", "user.undo"}; !reflect.DeepEqual(h.log, want) {
		t.Errorf("log = %v, want %v", h.log, want)
	}
}

func TestUninstallDryRunUndoesNothing(t *testing.T) {
	h := &harness{}
	m := undoManifest(Item{ID: "user", Kind: KindUser, Name: "fcjail", State: Created})
	ch := make(chan Event, 10)
	if err := uninstall(t, h, []Step{h.step("user", true)}, m, Options{DryRun: true, Events: ch}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	close(ch)
	if len(h.log) != 0 {
		t.Errorf("dry run called %v", h.log)
	}
	var kinds []EventKind
	for e := range ch {
		kinds = append(kinds, e.Kind)
	}
	if want := []EventKind{StepStarted, StepPlanned}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("events = %v, want %v", kinds, want)
	}
}

func TestUninstallWithoutAManifest(t *testing.T) {
	h := &harness{}
	if err := uninstall(t, h, []Step{h.step("user", true)}, nil, Options{}); !errors.Is(err, ErrNoManifest) {
		t.Errorf("err = %v, want ErrNoManifest", err)
	}
}

func TestUninstallWithNothingCreatedNeverElevates(t *testing.T) {
	h := &harness{}
	m := undoManifest(Item{ID: "packages", Kind: KindPackage, Name: "git", State: Adopted})
	if err := uninstall(t, h, []Step{h.step("packages", true)}, m, Options{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(h.log) != 0 {
		t.Errorf("log = %v, want nothing", h.log)
	}
}
