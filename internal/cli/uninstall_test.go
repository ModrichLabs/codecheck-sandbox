package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/setup"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// undoStep records what Undo got.
type undoStep struct {
	id     string
	undone map[string][]step.Item
}

func (u undoStep) ID() string                                 { return u.id }
func (u undoStep) Title() string                              { return "Step " + u.id }
func (u undoStep) Phase() step.Phase                          { return step.PhaseHost }
func (u undoStep) Check(context.Context) (step.Status, error) { return step.Status{OK: true}, nil }
func (u undoStep) Plan(context.Context) (step.Plan, error)    { return step.Plan{}, nil }
func (u undoStep) Apply(context.Context) error                { return nil }
func (u undoStep) Undo(_ context.Context, items []step.Item) error {
	u.undone[u.id] = append(u.undone[u.id], items...)
	return nil
}

type uninstallHost struct {
	undone          map[string][]step.Item
	manifest        *step.Manifest
	manifestRemoved bool
	present         map[string]bool
}

func (h *uninstallHost) env(t *testing.T) Env {
	env := testEnv()
	env.Home = t.TempDir()
	env.Environ["HOME"] = env.Home
	env.Setup = func(Env, Options) (*SetupHost, error) {
		ids := []string{"components", "host-jail-user", "host-rules", "host-tap-service", "guest-image"}
		return &SetupHost{
			Steps: func(setup.Deps) ([]step.Step, error) {
				var out []step.Step
				for _, id := range ids {
					out = append(out, undoStep{id: id, undone: h.undone})
				}
				return out, nil
			},
			LoadManifest:   func() (*step.Manifest, error) { return h.manifest, nil },
			RemoveManifest: func(context.Context) error { h.manifestRemoved = true; return nil },
			Exists:         func(p string) bool { return h.present[p] },
			UserExists:     func(n string) bool { return h.present["user:"+n] },
			Lock:           func(context.Context) (func(), error) { return func() {}, nil },
		}, nil
	}
	return env
}

func TestUninstallAfterSetupEndsClean(t *testing.T) {
	h := &uninstallHost{undone: map[string][]step.Item{}, manifest: &step.Manifest{Version: step.ManifestVersion, Items: []step.Item{
		{ID: "components", Kind: step.KindPackage, Name: "git", State: step.Created},
		{ID: "components", Kind: step.KindPackage, Name: "nftables", State: step.Adopted},
		{ID: "host-jail-user", Kind: step.KindUser, Name: "fcjail", State: step.Created},
		{ID: "host-rules", Kind: step.KindFile, Path: "/etc/codecheck-sandbox/rules.nft", State: step.Created},
	}}}
	env := h.env(t)
	reports := filepath.Join(env.Home, "codecheck-reports")
	os.MkdirAll(filepath.Join(reports, "r1"), 0o700)
	out, err := runIn(t, env, "uninstall", "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if got := h.undone["components"]; len(got) != 1 || got[0].Name != "git" {
		t.Errorf("components undid %v; the adopted nftables must stay", got)
	}
	if len(h.undone["host-jail-user"]) != 1 || len(h.undone["host-rules"]) != 1 || len(h.undone["guest-image"]) != 0 {
		t.Errorf("undone = %v", h.undone)
	}
	if !h.manifestRemoved {
		t.Error("the manifest outlived a clean uninstall")
	}
	if _, err := os.Stat(reports); err != nil {
		t.Error("uninstall removed the reports without --purge")
	}
	if !strings.Contains(out, "nftables") {
		t.Errorf("the list doesn't say the adopted package stays:\n%s", out)
	}
}

func TestUninstallPurgeTakesTheReports(t *testing.T) {
	h := &uninstallHost{undone: map[string][]step.Item{}, manifest: &step.Manifest{Version: step.ManifestVersion}}
	env := h.env(t)
	for _, d := range []string{"codecheck-reports/r1", "codecheck-work/x", ".cache/codecheck-sandbox/build"} {
		os.MkdirAll(filepath.Join(env.Home, d), 0o700)
	}
	if _, err := runIn(t, env, "uninstall", "--yes", "--purge"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"codecheck-reports", "codecheck-work", ".cache/codecheck-sandbox"} {
		if _, err := os.Stat(filepath.Join(env.Home, d)); err == nil {
			t.Errorf("--purge left %s", d)
		}
	}
}

// Without a manifest, uninstall scans the footprint: it removes the
// project's own items it finds, and never a package.
func TestUninstallFallsBackToTheFootprint(t *testing.T) {
	h := &uninstallHost{undone: map[string][]step.Item{}, present: map[string]bool{
		"/etc/codecheck-sandbox/rules.nft":          true,
		"/etc/systemd/system/codecheck-net.service": true,
		"user:fcjail": true,
	}}
	out, err := runIn(t, h.env(t), "uninstall", "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if !strings.Contains(out, "footprint") {
		t.Errorf("no word about the fallback:\n%s", out)
	}
	var rules []string
	for _, it := range h.undone["host-rules"] {
		rules = append(rules, it.Path+it.Name)
	}
	if !slices.Contains(rules, "/etc/codecheck-sandbox/rules.nft") || !slices.Contains(rules, "inet codecheck") {
		t.Errorf("host-rules undid %v", rules)
	}
	if slices.Contains(rules, "/var/lib/codecheck-sandbox/rules.sha256") {
		t.Error("the scan undid a file that isn't there")
	}
	if len(h.undone["host-jail-user"]) != 1 || len(h.undone["host-tap-service"]) < 2 {
		t.Errorf("undone = %v", h.undone)
	}
	if len(h.undone["components"]) != 0 {
		t.Error("the footprint scan removed packages")
	}
}

func TestUninstallAsksFirst(t *testing.T) {
	h := &uninstallHost{undone: map[string][]step.Item{}, manifest: &step.Manifest{Version: step.ManifestVersion, Items: []step.Item{
		{ID: "host-rules", Kind: step.KindFile, Path: "/etc/codecheck-sandbox/rules.nft", State: step.Created},
	}}}
	if _, err := runIn(t, h.env(t), "uninstall"); err == nil {
		t.Error("uninstall ran without --yes or a terminal to ask")
	}
	if len(h.undone) != 0 {
		t.Error("it undid something anyway")
	}
	out, err := runIn(t, h.env(t), "uninstall", "--dry-run")
	if err != nil || len(h.undone) != 0 || h.manifestRemoved || !strings.Contains(out, "rules.nft") {
		t.Errorf("dry run: %v, undone %v\n%s", err, h.undone, out)
	}
}
