package distro

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// lookFor finds only the named tools.
func lookFor(present ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(present, name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestComponentsPassWhenEveryToolExists(t *testing.T) {
	s := ComponentsStep(ComponentsDeps{Family: Arch, Look: lookFor("nft", "mkfs.ext4", "debugfs", "unsquashfs", "git", "ip", "gpg")})
	st, err := s.Check(context.Background())
	if err != nil || !st.OK {
		t.Errorf("Check = %+v, %v", st, err)
	}
	if s.Phase() != step.PhaseComponents {
		t.Errorf("phase %s", s.Phase())
	}
}

func TestComponentsInstallOnlyWhatsMissing(t *testing.T) {
	r := &system.FakeRunner{}
	var recorded []step.Item
	look := lookFor("nft", "mkfs.ext4", "debugfs", "git", "ip")
	s := ComponentsStep(ComponentsDeps{Family: Fedora, Root: r, Look: look, Record: func(i step.Item) { recorded = append(recorded, i) }})
	st, _ := s.Check(context.Background())
	if st.OK || !strings.Contains(st.Detail, "squashfs-tools") || !strings.Contains(st.Detail, "gnupg") {
		t.Errorf("Check = %+v", st)
	}
	p, err := s.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"dnf", "install", "-y", "squashfs-tools", "gnupg2"}
	if !p.NeedsRoot || len(p.Cmds) != 1 || !p.Cmds[0].Root || !slices.Equal(append([]string{p.Cmds[0].Name}, p.Cmds[0].Args...), want) {
		t.Errorf("plan = %+v", p)
	}
	r.On("dnf", "install").Return(system.Result{})
	if err := s.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var names []string
	for _, it := range recorded {
		if it.Kind != step.KindPackage || it.State != step.Created || it.ID != s.ID() {
			t.Errorf("item %+v", it)
		}
		names = append(names, it.Name)
	}
	if !slices.Equal(names, []string{"squashfs-tools", "gnupg2"}) {
		t.Errorf("recorded %v", names)
	}
	for _, n := range names {
		if err := step.CheckName(step.KindPackage, n); err != nil {
			t.Errorf("the manifest would refuse %s: %v", n, err)
		}
	}
}

func TestComponentsReportAFailedInstall(t *testing.T) {
	r := &system.FakeRunner{}
	r.On("pacman").Return(system.Result{Code: 1, Stderr: []byte("error: target not found: gnupg")})
	s := ComponentsStep(ComponentsDeps{Family: Arch, Root: r, Look: lookFor("nft"), Record: func(step.Item) { t.Error("recorded a failed install") }})
	if err := s.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "target not found") {
		t.Errorf("Apply = %v", err)
	}
}

func TestComponentsOnAnUnknownDistroListThePackages(t *testing.T) {
	s := ComponentsStep(ComponentsDeps{Family: "", Look: lookFor()})
	_, err := s.Plan(context.Background())
	if !errors.Is(err, ErrUnknownFamily) || !strings.Contains(err.Error(), "nftables") {
		t.Errorf("Plan = %v", err)
	}
}

func TestComponentsUndoRemovesOnlyWhatItInstalled(t *testing.T) {
	r := &system.FakeRunner{}
	r.On("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "remove").Return(system.Result{})
	s := ComponentsStep(ComponentsDeps{Family: Debian, Root: r, Look: lookFor()})
	err := s.Undo(context.Background(), []step.Item{
		{ID: s.ID(), Kind: step.KindPackage, Name: "git", State: step.Created},
		{ID: s.ID(), Kind: step.KindPackage, Name: "gnupg", State: step.Created},
	})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	calls := r.Calls()
	if len(calls) != 1 || !calls[0].Root || !slices.Equal(calls[0].Args, []string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "remove", "-y", "git", "gnupg"}) {
		t.Errorf("calls = %+v", calls)
	}
	// A name outside the allowlist never reaches the package manager.
	bad := s.Undo(context.Background(), []step.Item{{ID: s.ID(), Kind: step.KindPackage, Name: "openssh-server", State: step.Created}})
	if bad == nil {
		t.Error("Undo removed a package setup never installs")
	}
}
