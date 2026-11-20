package step

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// undoStep fails its Undo when err is set.
type undoStep struct {
	fakeStep
	err error
}

func (s *undoStep) Undo(ctx context.Context) error {
	_ = s.fakeStep.Undo(ctx)
	return s.err
}

func TestUninstallUndoesCreatedStepsInReverse(t *testing.T) {
	var log []string
	mk := func(id string) *undoStep { return &undoStep{fakeStep: fakeStep{id: id, log: &log}} }
	steps := []Step{mk("user"), mk("packages"), mk("rules"), mk("proxy"), mk("forgotten")}
	m := &Manifest{Version: ManifestVersion, Items: []Item{
		{ID: "user", Kind: KindUser, Name: "fcjail", State: Created},
		{ID: "packages", Kind: KindPackage, Name: "nftables", State: Adopted},
		{ID: "rules", Kind: KindFile, Path: "/etc/codecheck-sandbox/rules.nft", State: Created},
		{ID: "proxy", Kind: KindUnit, Name: "codecheck-proxy.service", State: Created},
		{ID: "stale", Kind: KindUnit, Name: "old.service", State: Created}, // no such step
	}}

	if err := Uninstall(context.Background(), steps, m); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	want := []string{"proxy.undo", "rules.undo", "user.undo"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("log = %v, want %v", log, want)
	}
}

func TestUninstallUndoesAStepWithAnyCreatedItem(t *testing.T) {
	var log []string
	s := &undoStep{fakeStep: fakeStep{id: "packages", log: &log}}
	m := &Manifest{Version: ManifestVersion, Items: []Item{
		{ID: "packages", Kind: KindPackage, Name: "nftables", State: Adopted},
		{ID: "packages", Kind: KindPackage, Name: "squashfs-tools", State: Created},
	}}
	if err := Uninstall(context.Background(), []Step{s}, m); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !reflect.DeepEqual(log, []string{"packages.undo"}) {
		t.Errorf("log = %v, want [packages.undo]", log)
	}
}

func TestUninstallKeepsGoingAfterAFailure(t *testing.T) {
	var log []string
	boom := errors.New("boom")
	steps := []Step{
		&undoStep{fakeStep: fakeStep{id: "a", log: &log}},
		&undoStep{fakeStep: fakeStep{id: "b", log: &log}, err: boom},
		&undoStep{fakeStep: fakeStep{id: "c", log: &log}},
	}
	m := &Manifest{Version: ManifestVersion, Items: []Item{
		{ID: "a", Kind: KindUser, Name: "x", State: Created},
		{ID: "b", Kind: KindUser, Name: "y", State: Created},
		{ID: "c", Kind: KindUser, Name: "z", State: Created},
	}}
	err := Uninstall(context.Background(), steps, m)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if want := []string{"c.undo", "b.undo", "a.undo"}; !reflect.DeepEqual(log, want) {
		t.Errorf("log = %v, want %v", log, want)
	}
}
