// Package config reads and writes the user's settings,
// ~/.config/codecheck-sandbox/config.toml: the default budget, the reports
// folder, the CodeCheck commit and the telemetry choice. Every value gets
// checked on load and on save, so a hand-edited file can't smuggle in
// anything the rest of the program wouldn't accept.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Config holds the settings. Telemetry stays nil until the user has
// answered the consent question.
type Config struct {
	BudgetUSD       string `toml:"budget_usd"`
	ReportsDir      string `toml:"reports_dir"`
	CodeCheckCommit string `toml:"codecheck_commit"`
	Telemetry       *bool  `toml:"telemetry,omitempty"`
}

// Defaults returns the settings a fresh install starts with.
func Defaults() Config {
	return Config{BudgetUSD: "25", ReportsDir: "~/codecheck-reports", CodeCheckCommit: pins.CodeCheck.Commit}
}

// Path names config.toml from the environment: $XDG_CONFIG_HOME when it
// holds an absolute path (the XDG spec ignores a relative one), or
// ~/.config.
func Path(env map[string]string) string {
	base := env["XDG_CONFIG_HOME"]
	if !filepath.IsAbs(base) {
		base = filepath.Join(env["HOME"], ".config")
	}
	return filepath.Join(base, "codecheck-sandbox", "config.toml")
}

// StateDir names the folder for logs and the telemetry queue:
// $XDG_STATE_HOME/codecheck-sandbox, or ~/.local/state/codecheck-sandbox.
func StateDir(env map[string]string) string {
	base := env["XDG_STATE_HOME"]
	if !filepath.IsAbs(base) {
		base = filepath.Join(env["HOME"], ".local", "state")
	}
	return filepath.Join(base, "codecheck-sandbox")
}

// Load reads path (home expands ~ for the checks). A missing file gives the defaults and found=false; a
// partial file keeps the defaults for what it leaves out. Unknown keys and
// invalid values fail the load.
func Load(fsys system.FS, path, home string) (c Config, found bool, err error) {
	c = Defaults()
	b, err := fsys.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return Defaults(), true, fmt.Errorf("%s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return Defaults(), true, fmt.Errorf("%s: unknown setting %q", path, extra[0].String())
	}
	if err := c.Validate(home); err != nil {
		return Defaults(), true, fmt.Errorf("%s: %w", path, err)
	}
	return c, true, nil
}

// Save writes c to path with mode 0600, in a 0700 folder.
func Save(fsys system.FS, path, home string, c Config) error {
	if err := c.Validate(home); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# codecheck-sandbox settings; `codecheck-sandbox setup` writes this file.\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if err := fsys.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsys.WriteFile(path, buf.Bytes(), 0o600)
}

var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Validate checks every setting; home expands ~ in the reports folder.
func (c Config) Validate(home string) error {
	if _, err := NormaliseBudget(c.BudgetUSD); err != nil {
		return fmt.Errorf("budget_usd: %w", err)
	}
	if _, err := ExpandReportsDir(c.ReportsDir, home); err != nil {
		return fmt.Errorf("reports_dir: %w", err)
	}
	if !commitPattern.MatchString(c.CodeCheckCommit) {
		return errors.New("codecheck_commit: want a full commit hash")
	}
	return nil
}

// ReportsPath expands ~ in the reports folder against home.
func (c Config) ReportsPath(home string) (string, error) {
	return ExpandReportsDir(c.ReportsDir, home)
}

// MaxBudget caps a review's budget in US dollars.
const MaxBudget = 1000

var budgetPattern = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,2})?$`)

// NormaliseBudget reads a budget such as 25, $25 or US$7.50 and returns the
// digits cc-launch and the guest accept: above zero, at most MaxBudget, and
// no more than two decimal places.
func NormaliseBudget(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "US"), "$")
	if !budgetPattern.MatchString(s) {
		return "", errors.New("enter an amount in US dollars, such as 25")
	}
	v, _ := strconv.ParseFloat(s, 64)
	switch {
	case v <= 0 || math.IsNaN(v):
		return "", errors.New("the budget has to exceed zero")
	case v > MaxBudget:
		return "", fmt.Errorf("budgets go up to US$%d", MaxBudget)
	}
	return s, nil
}

// systemDirs hold files the system owns; reports never go there.
var systemDirs = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var"}

// ExpandReportsDir turns ~ into home and checks the result: an absolute
// path, free of control characters, outside the system's own folders.
func ExpandReportsDir(s, home string) (string, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("the folder name holds control characters")
		}
	}
	switch {
	case s == "~":
		s = home
	case strings.HasPrefix(s, "~/"):
		s = filepath.Join(home, s[2:])
	}
	if !filepath.IsAbs(s) {
		return "", errors.New("give a full path, such as ~/codecheck-reports")
	}
	s = filepath.Clean(s)
	if s == "/" {
		return "", errors.New("pick a folder of your own, such as ~/codecheck-reports")
	}
	// Inside the user's own home always passes, even where homes live
	// under a system folder (/var/home on Fedora Silverblue).
	if h := filepath.Clean(home); filepath.IsAbs(h) && h != "/" && (s == h || strings.HasPrefix(s, h+"/")) {
		return s, nil
	}
	for _, d := range systemDirs {
		if s == d || strings.HasPrefix(s, d+"/") {
			return "", fmt.Errorf("%s belongs to the system; pick a folder of your own", d)
		}
	}
	return s, nil
}
