package launch

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestStageRunInstallsRootOwnedCopies(t *testing.T) {
	f := &system.FakeRunner{}
	for range 4 {
		f.On("install").Return(system.Result{})
	}
	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	p := DefaultPaths()
	if err := StageRun(context.Background(), f, p, id, "/home/u/work", []string{"config.img", "code.ext4"}); err != nil {
		t.Fatalf("StageRun: %v", err)
	}
	var got []string
	for _, c := range f.Calls() {
		if !c.Root {
			t.Errorf("%s without the privilege tool", c.Name)
		}
		got = append(got, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	run := id.Dir(p)
	want := []string{
		"install -d -m 0755 -o root -g root " + p.RunsDir,
		"install -d -m 0755 -o root -g root " + run,
		"install -m 0644 -o root -g root -T /home/u/work/config.img " + run + "/config.img",
		"install -m 0644 -o root -g root -T /home/u/work/code.ext4 " + run + "/code.ext4",
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStageRunRefusesOddNames(t *testing.T) {
	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	for _, name := range []string{"../escape", "a/b", "", "."} {
		if err := StageRun(context.Background(), &system.FakeRunner{}, DefaultPaths(), id, "/w", []string{name}); err == nil {
			t.Errorf("StageRun accepted %q", name)
		}
	}
}

func TestStageRunStopsAtAFailure(t *testing.T) {
	f := &system.FakeRunner{}
	f.On("install").Return(system.Result{Code: 1, Stderr: []byte("sudo: a password is required")})
	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	if err := StageRun(context.Background(), f, DefaultPaths(), id, "/w", []string{"config.img"}); err == nil {
		t.Error("StageRun hid the failure")
	}
	if len(f.Calls()) != 1 {
		t.Errorf("ran %d commands after the failure", len(f.Calls()))
	}
}
