package review

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func needTools(t *testing.T) string {
	t.Helper()
	if _, err := system.LookTool("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4 here")
	}
	d, err := system.LookTool("debugfs")
	if err != nil {
		t.Skip("no debugfs here")
	}
	return d
}

func debugfsCat(t *testing.T, debugfs, disk, path string) string {
	t.Helper()
	res, err := system.ExecRunner{}.Run(context.Background(), system.Cmd{Name: debugfs, Args: []string{"-R", "cat " + path, disk}})
	if err != nil {
		t.Fatal(err)
	}
	return string(res.Stdout)
}

func TestMakeRunFilesForADiffReview(t *testing.T) {
	debugfs := needTools(t)
	src := repo(t, "")
	gitT(t, src, "checkout", "-q", "-b", "feature")
	commitFile(t, src, "new.go", "package app\n", "add")
	p, err := Prepare(context.Background(), system.ExecRunner{}, t.TempDir(), Source{Target: src, Base: "main"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := guestinit.Config{Mode: guestinit.ModeReview, RunID: "cc-20261121-101500-a1b2c3", MaxBudgetUSD: "25", Scope: p.Scope, Base: p.Base}
	if err := MakeRunFiles(context.Background(), system.ExecRunner{}, p.Dir, filepath.Join(p.Dir, "code"), cfg); err != nil {
		t.Fatalf("MakeRunFiles: %v", err)
	}

	block, _ := os.ReadFile(filepath.Join(p.Dir, "config.img"))
	got, err := guestinit.Parse(block)
	if err != nil || got.Base != p.Base || got.Scope != guestinit.ScopeDiff || got.RunID != cfg.RunID {
		t.Errorf("config.img parses to %+v, %v", got, err)
	}
	disk := filepath.Join(p.Dir, "code.ext4")
	if s := debugfsCat(t, debugfs, disk, "/src/new.go"); s != "package app\n" {
		t.Errorf("/src/new.go on the disk = %q", s)
	}
	if s := debugfsCat(t, debugfs, disk, "/change/diff.patch"); !strings.Contains(s, "new.go") {
		t.Errorf("/change/diff.patch on the disk = %q", s)
	}
}

func TestMakeRunFilesForASelftest(t *testing.T) {
	debugfs := needTools(t)
	dir := t.TempDir()
	code := filepath.Join(dir, "code")
	cfg := guestinit.Config{Mode: guestinit.ModeSelftest, RunID: "cc-20261121-101500-a1b2c3", ProbePorts: []int{8080}}
	if err := MakeRunFiles(context.Background(), system.ExecRunner{}, dir, code, cfg); err != nil {
		t.Fatalf("MakeRunFiles: %v", err)
	}
	// An empty code disk still mounts, so the probe checks a real mount.
	res, _ := system.ExecRunner{}.Run(context.Background(), system.Cmd{Name: debugfs, Args: []string{"-R", "ls -p /", filepath.Join(dir, "code.ext4")}})
	if !strings.Contains(string(res.Stdout), "/src/") {
		t.Errorf("selftest code disk root:\n%s", res.Stdout)
	}
}

func TestMakeRunFilesRefusesABadConfig(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "code", "src"), 0o755)
	bad := guestinit.Config{Mode: guestinit.ModeReview, RunID: "cc-20261121-101500-a1b2c3", MaxBudgetUSD: "lots", Scope: guestinit.ScopeFull}
	if err := MakeRunFiles(context.Background(), system.ExecRunner{}, dir, filepath.Join(dir, "code"), bad); err == nil {
		t.Error("MakeRunFiles accepted a budget the guest would refuse")
	}
	if _, err := os.Stat(filepath.Join(dir, "code.ext4")); !os.IsNotExist(err) {
		t.Error("a disk got made for a config that failed")
	}
}

// Many small files need blocks and inodes, not just their bytes.
func TestMakeRunFilesWithManySmallFiles(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "code", "src")
	for i := range 20 {
		sub := filepath.Join(src, "d"+strconv.Itoa(i))
		os.MkdirAll(sub, 0o755)
		for j := range 1000 {
			os.WriteFile(filepath.Join(sub, strconv.Itoa(j)+".txt"), []byte("x"), 0o644)
		}
	}
	cfg := guestinit.Config{Mode: guestinit.ModeReview, RunID: "cc-20261121-101500-a1b2c3", MaxBudgetUSD: "5", Scope: guestinit.ScopeFull}
	if err := MakeRunFiles(context.Background(), system.ExecRunner{}, dir, filepath.Join(dir, "code"), cfg); err != nil {
		t.Fatalf("MakeRunFiles with 20,000 small files: %v", err)
	}
}

func TestTooLargeMessageShowsTheDiskSize(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	code := filepath.Join(dir, "code")
	os.MkdirAll(filepath.Join(code, "src"), 0o755)
	f, _ := os.Create(filepath.Join(code, "src", "big"))
	f.Truncate(launch.MaxCodeDisk * 8 / 10) // fits as bytes, not with the overhead
	f.Close()
	cfg := guestinit.Config{Mode: guestinit.ModeReview, RunID: "cc-20261121-101500-a1b2c3", MaxBudgetUSD: "5", Scope: guestinit.ScopeFull}
	err := MakeRunFiles(context.Background(), system.ExecRunner{}, dir, code, cfg)
	if err == nil {
		t.Fatal("MakeRunFiles accepted a disk past the cap")
	}
	if !strings.Contains(err.Error(), "needs a") {
		t.Errorf("error = %v; want it to give the disk size it needs", err)
	}
}
