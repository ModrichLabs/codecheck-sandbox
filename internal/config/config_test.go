package config

import (
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

const path = "/home/u/.config/codecheck-sandbox/config.toml"

func TestMissingFileGivesDefaults(t *testing.T) {
	c, found, err := Load(system.NewMemFS(), path, "/home/u")
	if err != nil || found {
		t.Fatalf("Load: found %v, %v", found, err)
	}
	if c.BudgetUSD != "25" || c.ReportsDir != "~/codecheck-reports" || c.CodeCheckCommit != pins.CodeCheck.Commit || c.Telemetry != nil {
		t.Errorf("defaults = %+v", c)
	}
}

func TestRoundTrip(t *testing.T) {
	fsys := system.NewMemFS()
	on := true
	want := Config{BudgetUSD: "7.5", ReportsDir: "/data/reports", CodeCheckCommit: strings.Repeat("a", 40), Telemetry: &on}
	if err := Save(fsys, path, "/home/u", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, found, err := Load(fsys, path, "/home/u")
	if err != nil || !found {
		t.Fatalf("Load: found %v, %v", found, err)
	}
	if got.BudgetUSD != want.BudgetUSD || got.ReportsDir != want.ReportsDir || got.CodeCheckCommit != want.CodeCheckCommit || got.Telemetry == nil || !*got.Telemetry {
		t.Errorf("round trip = %+v", got)
	}
	fi, _ := fsys.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600", fi.Mode().Perm())
	}
	dir, _ := fsys.Stat("/home/u/.config/codecheck-sandbox")
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("config folder mode %v, want 0700", dir.Mode().Perm())
	}
}

// A partial file keeps the defaults for what it leaves out.
func TestPartialFileKeepsDefaults(t *testing.T) {
	fsys := system.NewMemFS()
	fsys.MkdirAll("/home/u/.config/codecheck-sandbox", 0o700)
	fsys.WriteFile(path, []byte("telemetry = false\n"), 0o600)
	c, _, err := Load(fsys, path, "/home/u")
	if err != nil || c.BudgetUSD != "25" || c.Telemetry == nil || *c.Telemetry {
		t.Errorf("Load = %+v, %v", c, err)
	}
}

func TestLoadRefusesWhatItCannotTrust(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":   "budget_usd = \"25\"\nbudgte = 3\n",
		"bad budget":    "budget_usd = \"lots\"\n",
		"zero budget":   "budget_usd = \"0\"\n",
		"huge budget":   "budget_usd = \"5000\"\n",
		"bad commit":    "codecheck_commit = \"main\"\n",
		"relative dir":  "reports_dir = \"reports\"\n",
		"system dir":    "reports_dir = \"/etc/reports\"\n",
		"not toml":      "budget_usd = \n",
		"wrong type":    "telemetry = \"yes\"\n",
		"control chars": "reports_dir = \"/data/\\u001b[2J\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := system.NewMemFS()
			fsys.MkdirAll("/home/u/.config/codecheck-sandbox", 0o700)
			fsys.WriteFile(path, []byte(body), 0o600)
			if _, _, err := Load(fsys, path, "/home/u"); err == nil {
				t.Error("Load accepted it")
			}
		})
	}
}

func TestSaveRefusesAnInvalidConfig(t *testing.T) {
	c := Defaults()
	c.BudgetUSD = "-1"
	if err := Save(system.NewMemFS(), path, "/home/u", c); err == nil {
		t.Error("Save wrote an invalid budget")
	}
}

func TestPathFollowsXDG(t *testing.T) {
	if got := Path(map[string]string{"HOME": "/home/u"}); got != "/home/u/.config/codecheck-sandbox/config.toml" {
		t.Errorf("Path = %s", got)
	}
	if got := Path(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "/cfg"}); got != "/cfg/codecheck-sandbox/config.toml" {
		t.Errorf("Path = %s", got)
	}
	// A relative XDG value breaks the spec, so it gets ignored.
	if got := Path(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "cfg"}); got != "/home/u/.config/codecheck-sandbox/config.toml" {
		t.Errorf("Path = %s", got)
	}
	if got := StateDir(map[string]string{"HOME": "/home/u"}); got != "/home/u/.local/state/codecheck-sandbox" {
		t.Errorf("StateDir = %s", got)
	}
}

func TestExpandReportsDir(t *testing.T) {
	c := Defaults()
	if got, err := c.ReportsPath("/home/u"); err != nil || got != "/home/u/codecheck-reports" {
		t.Errorf("ReportsPath = %s, %v", got, err)
	}
}

// Fedora Silverblue and other image-based systems keep homes in /var/home.
func TestReportsInsideHomeAlwaysPass(t *testing.T) {
	for _, in := range []string{"~/codecheck-reports", "/var/home/u/reports"} {
		if got, err := ExpandReportsDir(in, "/var/home/u"); err != nil || !strings.HasPrefix(got, "/var/home/u/") {
			t.Errorf("ExpandReportsDir(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ExpandReportsDir("/var/lib/x", "/var/home/u"); err == nil {
		t.Error("a system folder outside home passed")
	}
}

func TestBudgetNormalises(t *testing.T) {
	for in, want := range map[string]string{"25": "25", "$25": "25", "US$7.50": "7.50", " 10 ": "10"} {
		if got, err := NormaliseBudget(in); err != nil || got != want {
			t.Errorf("NormaliseBudget(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "1.234", "1e3", "0.00", "1001", "abc"} {
		if _, err := NormaliseBudget(bad); err == nil {
			t.Errorf("NormaliseBudget(%q) passed", bad)
		}
	}
}
