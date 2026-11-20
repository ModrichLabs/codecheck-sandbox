// Package guestinit holds the guest's PID 1 logic: it reads the run settings
// from the raw config device, then builds the guest image, runs a review or
// runs the escape probes, and leaves the reboot to its caller.
package guestinit

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// BlockSize fixes the config device's settings block at 4 KB: key=value lines,
// each ending in a newline, then NUL bytes up to the end.
const BlockSize = 4096

// maxValue caps every value, so no setting can smuggle a payload.
const maxValue = 128

// Mode picks what the guest does.
type Mode string

// The three modes.
const (
	ModeBuild    Mode = "build"
	ModeReview   Mode = "review"
	ModeSelftest Mode = "selftest"
)

// Scope picks between a whole-tree review and a diff-aware one.
type Scope string

// The two review scopes.
const (
	ScopeFull Scope = "full"
	ScopeDiff Scope = "diff"
)

// Config holds the run settings the host wrote to the config device.
type Config struct {
	Mode         Mode
	RunID        string
	MaxBudgetUSD string // a decimal string, passed to --max-budget-usd as is
	Scope        Scope  // review only
	Base         string // diff reviews only: the baseline commit
	ProbePorts   []int  // selftest only: extra host ports to try
}

var (
	runIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	budgetPattern = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,2})?$`)
	basePattern   = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// keyOrder lists every key the block may hold, in the order Encode writes them.
var keyOrder = []string{"mode", "run_id", "max_budget_usd", "scope", "base", "probe_ports"}

// Parse reads a settings block. It refuses anything it does not fully
// understand: unknown or repeated keys, stray bytes, and values that fail
// their pattern or don't belong to the mode.
func Parse(b []byte) (Config, error) {
	if len(b) != BlockSize {
		return Config{}, fmt.Errorf("config block holds %d bytes, want %d", len(b), BlockSize)
	}
	text, pad, _ := bytes.Cut(b, []byte{0})
	if len(bytes.Trim(pad, "\x00")) != 0 {
		return Config{}, errors.New("config block: bytes after the padding")
	}
	if len(text) == 0 || text[len(text)-1] != '\n' {
		return Config{}, errors.New("config block: the last line lacks a newline")
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(text[:len(text)-1]), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("config line %q: want key=value", line)
		}
		if !knownKey(key) {
			return Config{}, fmt.Errorf("config key %q: unknown", key)
		}
		if _, dup := kv[key]; dup {
			return Config{}, fmt.Errorf("config key %q: repeated", key)
		}
		if len(value) > maxValue {
			return Config{}, fmt.Errorf("config key %q: value longer than %d bytes", key, maxValue)
		}
		kv[key] = value
	}

	c := Config{
		Mode:         Mode(kv["mode"]),
		RunID:        kv["run_id"],
		MaxBudgetUSD: kv["max_budget_usd"],
		Scope:        Scope(kv["scope"]),
		Base:         kv["base"],
	}
	if p, ok := kv["probe_ports"]; ok {
		ports, err := parsePorts(p)
		if err != nil {
			return Config{}, err
		}
		c.ProbePorts = ports
		if len(ports) == 0 {
			return Config{}, errors.New("config key \"probe_ports\": empty")
		}
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks every field against its pattern and the mode's needs.
func (c Config) Validate() error {
	if !runIDPattern.MatchString(c.RunID) {
		return fmt.Errorf("run_id %q: want lower-case letters, digits and dashes", c.RunID)
	}
	switch c.Mode {
	case ModeBuild, ModeSelftest:
		if c.MaxBudgetUSD != "" || c.Scope != "" || c.Base != "" {
			return fmt.Errorf("mode %s takes no budget, scope or base", c.Mode)
		}
		if c.Mode == ModeBuild && len(c.ProbePorts) > 0 {
			return errors.New("mode build takes no probe_ports")
		}
	case ModeReview:
		if len(c.ProbePorts) > 0 {
			return errors.New("mode review takes no probe_ports")
		}
		if !budgetPattern.MatchString(c.MaxBudgetUSD) {
			return fmt.Errorf("max_budget_usd %q: want a decimal amount", c.MaxBudgetUSD)
		}
		if v, _ := strconv.ParseFloat(c.MaxBudgetUSD, 64); v <= 0 {
			return errors.New("max_budget_usd: want more than zero")
		}
		switch c.Scope {
		case ScopeFull:
			if c.Base != "" {
				return errors.New("scope full takes no base")
			}
		case ScopeDiff:
			if !basePattern.MatchString(c.Base) {
				return fmt.Errorf("base %q: want a full commit hash", c.Base)
			}
		default:
			return fmt.Errorf("scope %q: want full or diff", c.Scope)
		}
	case "":
		return errors.New("config block: no mode")
	default:
		return fmt.Errorf("mode %q: want build, review or selftest", c.Mode)
	}
	return nil
}

// Encode writes c as a settings block, for the host side.
func Encode(c Config) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	values := map[string]string{
		"mode": string(c.Mode), "run_id": c.RunID, "max_budget_usd": c.MaxBudgetUSD,
		"scope": string(c.Scope), "base": c.Base,
	}
	if len(c.ProbePorts) > 0 {
		s := make([]string, len(c.ProbePorts))
		for i, p := range c.ProbePorts {
			s[i] = strconv.Itoa(p)
		}
		values["probe_ports"] = strings.Join(s, ",")
	}
	var buf bytes.Buffer
	for _, k := range keyOrder {
		if v := values[k]; v != "" {
			if len(v) > maxValue {
				return nil, fmt.Errorf("config key %q: value longer than %d bytes", k, maxValue)
			}
			fmt.Fprintf(&buf, "%s=%s\n", k, v)
		}
	}
	out := make([]byte, BlockSize)
	copy(out, buf.Bytes())
	return out, nil
}

func knownKey(k string) bool {
	for _, known := range keyOrder {
		if k == known {
			return true
		}
	}
	return false
}

// parsePorts reads a comma-separated list of TCP ports.
func parsePorts(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		p, err := strconv.Atoi(f)
		if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != f {
			return nil, fmt.Errorf("probe_ports %q: want ports from 1 to 65535", s)
		}
		out = append(out, p)
	}
	return out, nil
}
