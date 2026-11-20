// Package telemetry builds and sends the opt-in usage, error, environment and
// security events of spec §11.
//
// Safety comes from the types. An event field may only hold a bool or a type
// from this package that implements the unexported value interface: an enum,
// a band or a version. Each such type checks itself when encoded, so even a
// careless conversion such as Outcome(err.Error()) sends "other", never the
// text. Properties refuses any other field type.
package telemetry

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"time"
)

// Event names one telemetry event; its fields become the properties.
type Event interface {
	EventName() string
}

// value marks the types an event field may hold. The method stays
// unexported, so only this package can add such types.
type value interface {
	value() any
}

const (
	other   = "other"
	invalid = "invalid"
)

// oneOf returns s when allowed lists it, and "other" otherwise.
func oneOf(s string, allowed []string) string {
	if slices.Contains(allowed, s) {
		return s
	}
	return other
}

// --- Enums ---

// Command names a top-level CLI command.
type Command string

func (c Command) value() any {
	return oneOf(string(c), []string{"setup", "doctor", "selftest", "canary", "review", "rebuild", "uninstall", "telemetry", "version", "proxy"})
}

// Outcome says how something ended.
type Outcome string

const (
	OutcomeOK       Outcome = "ok"
	OutcomeFailed   Outcome = "failed"
	OutcomeDeclined Outcome = "declined"
	OutcomeSkipped  Outcome = "skipped"
)

func (o Outcome) value() any {
	return oneOf(string(o), []string{"ok", "failed", "declined", "skipped"})
}

// Phase names a setup wizard phase.
type Phase string

func (p Phase) value() any {
	return oneOf(string(p), []string{"platform", "components", "details", "host", "guest", "verify", "summary"})
}

// ReviewMode says what a review covered.
type ReviewMode string

func (m ReviewMode) value() any { return oneOf(string(m), []string{"full", "diff", "pr"}) }

// DistroFamily groups distributions by package manager.
type DistroFamily string

func (d DistroFamily) value() any {
	return oneOf(string(d), []string{"arch", "debian", "fedora", "opensuse"})
}

// Arch names the CPU architecture.
type Arch string

func (a Arch) value() any { return oneOf(string(a), []string{"amd64", "arm64"}) }

// FirewallManager names the firewall manager found on the host.
type FirewallManager string

func (f FirewallManager) value() any {
	return oneOf(string(f), []string{"none", "firewalld", "ufw", "docker", "several"})
}

// SecurityModule names the Linux security module in force.
type SecurityModule string

func (s SecurityModule) value() any {
	return oneOf(string(s), []string{"none", "selinux", "apparmor"})
}

// SecurityMode names the security module's mode.
type SecurityMode string

func (s SecurityMode) value() any {
	return oneOf(string(s), []string{"enforcing", "permissive", "complain", "disabled"})
}

// PrivilegeTool names the privilege tool in use.
type PrivilegeTool string

func (p PrivilegeTool) value() any {
	return oneOf(string(p), []string{"sudo", "sudo-rs", "doas", "run0"})
}

// Probe names a selftest probe.
type Probe string

func (p Probe) value() any {
	return oneOf(string(p), []string{"other-port", "ipv6-link-local", "docker-port", "dns", "spoofed-source", "outside-host"})
}

// RefusalReason names why cc-launch refused to start a VM.
type RefusalReason string

func (r RefusalReason) value() any {
	return oneOf(string(r), []string{"image_hash", "rule_hash", "listener_owner", "lock", "run_disks"})
}

// StepID names a setup step. Only IDs that step packages register count;
// every other value encodes as "other".
type StepID string

var (
	stepMu    sync.RWMutex
	stepIDs   = map[string]bool{}
	stepIDPat = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// RegisterStepIDs adds code-defined step IDs to the set telemetry may send.
// Step packages call it from init.
func RegisterStepIDs(ids ...string) {
	stepMu.Lock()
	defer stepMu.Unlock()
	for _, id := range ids {
		if !stepIDPat.MatchString(id) {
			panic(fmt.Sprintf("telemetry: step id %q must match %s", id, stepIDPat))
		}
		stepIDs[id] = true
	}
}

func (s StepID) value() any {
	stepMu.RLock()
	defer stepMu.RUnlock()
	if stepIDs[string(s)] {
		return string(s)
	}
	return other
}

// Version holds a release version such as 0.1.0-dev, v1.18.0, a kernel
// major.minor or a distro version id; anything else encodes as "invalid".
type Version string

// At most three numeric parts, since four would let an IPv4 address through,
// and only the dev, rc, alpha and beta suffixes, so no free text rides along.
var versionPattern = regexp.MustCompile(`^v?\d{1,5}(\.\d{1,5}){0,2}(-(dev|rc|alpha|beta)(\.?\d{1,3})?)?$`)

func (v Version) value() any {
	if string(v) == "rolling" || versionPattern.MatchString(string(v)) {
		return string(v)
	}
	return invalid
}

// --- Bands ---

// DurationBand buckets a duration.
type DurationBand string

var durationBands = []string{"<10s", "10s-1m", "1-5m", "5-15m", "15-60m", ">=60m"}

// DurationBandOf buckets d.
func DurationBandOf(d time.Duration) DurationBand {
	switch {
	case d < 10*time.Second:
		return "<10s"
	case d < time.Minute:
		return "10s-1m"
	case d < 5*time.Minute:
		return "1-5m"
	case d < 15*time.Minute:
		return "5-15m"
	case d < time.Hour:
		return "15-60m"
	}
	return ">=60m"
}

func (b DurationBand) value() any { return oneOf(string(b), durationBands) }

// CostBand buckets a cost in US dollars.
type CostBand string

var costBands = []string{"<$1", "$1-5", "$5-10", "$10-25", ">=$25"}

// CostBandOf buckets usd.
func CostBandOf(usd float64) CostBand {
	switch {
	case usd < 1:
		return "<$1"
	case usd < 5:
		return "$1-5"
	case usd < 10:
		return "$5-10"
	case usd < 25:
		return "$10-25"
	}
	return ">=$25"
}

func (b CostBand) value() any { return oneOf(string(b), costBands) }

// SizeBand buckets a memory or disk size.
type SizeBand string

var sizeBands = []string{"<8GB", "8-16GB", "16-32GB", "32-64GB", ">=64GB"}

// SizeBandOf buckets a size in bytes.
func SizeBandOf(bytes uint64) SizeBand {
	const gb = 1 << 30
	switch {
	case bytes < 8*gb:
		return "<8GB"
	case bytes < 16*gb:
		return "8-16GB"
	case bytes < 32*gb:
		return "16-32GB"
	case bytes < 64*gb:
		return "32-64GB"
	}
	return ">=64GB"
}

func (b SizeBand) value() any { return oneOf(string(b), sizeBands) }

// CountBand buckets a count, such as sub-agents or proxy denials.
type CountBand string

var countBands = []string{"0", "1", "2-5", "6-10", ">10"}

// CountBandOf buckets n.
func CountBandOf(n int) CountBand {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 5:
		return "2-5"
	case n <= 10:
		return "6-10"
	}
	return ">10"
}

func (b CountBand) value() any { return oneOf(string(b), countBands) }

// --- Events ---

// CommandRun records one CLI command.
type CommandRun struct {
	Command    Command      `prop:"command"`
	Outcome    Outcome      `prop:"outcome"`
	Duration   DurationBand `prop:"duration"`
	CLIVersion Version      `prop:"cli_version"`
}

func (CommandRun) EventName() string { return "command_run" }

// SetupPhaseReached records how far a setup run got.
type SetupPhaseReached struct {
	Phase Phase `prop:"phase"`
}

func (SetupPhaseReached) EventName() string { return "setup_phase_reached" }

// StepFinished records one step's outcome.
type StepFinished struct {
	Step     StepID       `prop:"step"`
	Outcome  Outcome      `prop:"outcome"`
	Duration DurationBand `prop:"duration"`
}

func (StepFinished) EventName() string { return "step_finished" }

// ReviewFinished records one review run, with its security signals.
type ReviewFinished struct {
	Mode          ReviewMode   `prop:"mode"`
	Outcome       Outcome      `prop:"outcome"`
	Duration      DurationBand `prop:"duration"`
	Cost          CostBand     `prop:"cost"`
	SubAgents     CountBand    `prop:"sub_agents"`
	ProxyDenials  CountBand    `prop:"proxy_denials"`
	CLIVersion    Version      `prop:"cli_version"`
	ClaudeVersion Version      `prop:"claude_version"`
}

func (ReviewFinished) EventName() string { return "review_finished" }

// EnvironmentProfile describes the host, once per setup and doctor.
type EnvironmentProfile struct {
	Distro             DistroFamily    `prop:"distro_family"`
	DistroVersion      Version         `prop:"distro_version"`
	Arch               Arch            `prop:"arch"`
	Kernel             Version         `prop:"kernel"`
	RAM                SizeBand        `prop:"ram"`
	Disk               SizeBand        `prop:"disk"`
	Firewall           FirewallManager `prop:"firewall"`
	SecurityModule     SecurityModule  `prop:"security_module"`
	SecurityMode       SecurityMode    `prop:"security_mode"`
	PrivilegeTool      PrivilegeTool   `prop:"privilege_tool"`
	FirecrackerVersion Version         `prop:"firecracker_version"`
}

func (EnvironmentProfile) EventName() string { return "environment_profile" }

// SelftestProbeFailed records a selftest probe that got through when it
// should not have.
type SelftestProbeFailed struct {
	Probe Probe `prop:"probe"`
}

func (SelftestProbeFailed) EventName() string { return "selftest_probe_failed" }

// LaunchRefused records a cc-launch refusal; the CLI sends it after reading
// the helper's exit code, since cc-launch itself never sends anything.
type LaunchRefused struct {
	Reason RefusalReason `prop:"reason"`
}

func (LaunchRefused) EventName() string { return "launch_refused" }

// AllEvents lists one zero value of every event type, for the allowlist test
// and the consent screen's sample payload.
func AllEvents() []Event {
	return []Event{
		CommandRun{}, SetupPhaseReached{}, StepFinished{}, ReviewFinished{},
		EnvironmentProfile{}, SelftestProbeFailed{}, LaunchRefused{},
	}
}

// Properties encodes e's fields by their prop tags. It refuses any field that
// holds neither a bool nor one of this package's value types.
func Properties(e Event) (map[string]any, error) {
	v := reflect.ValueOf(e)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("telemetry: event %T: want a struct", e)
	}
	t := v.Type()
	out := make(map[string]any, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		name := f.Tag.Get("prop")
		if name == "" {
			return nil, fmt.Errorf("telemetry: %T.%s lacks a prop tag", e, f.Name)
		}
		fv := v.Field(i)
		switch x := fv.Interface().(type) {
		case value:
			out[name] = x.value()
		case bool:
			out[name] = x
		default:
			return nil, fmt.Errorf("telemetry: %T.%s has type %s, which telemetry may not send", e, f.Name, f.Type)
		}
	}
	return out, nil
}
