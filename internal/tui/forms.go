package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/huh/v2"
)

// Details holds the "Your details" answers (spec §7, step 4). The telemetry
// answer comes from the consent screen.
type Details struct {
	APIKey     string
	Budget     string // US dollars per review, as typed; see ParseBudget
	ReportsDir string // as typed; see ExpandReportsDir
	VMSize     VMSize
}

// HostSize describes the machine, for scaling the VM size choices.
type HostSize struct {
	RAM  uint64
	CPUs int
}

// FormDeps supplies what the form's validators need.
type FormDeps struct {
	// CheckKey tests a key against Anthropic; nil checks the format only
	// (dry runs, and tests).
	CheckKey func(ctx context.Context, key string) error
	Host     HostSize
	// Home expands ~ in the reports folder.
	Home string
}

// DetailsForm asks for the API key, then the budget, reports folder and VM
// size, writing the answers into d.
func DetailsForm(d *Details, deps FormDeps) *huh.Form {
	sizes := VMSizes(deps.Host.RAM, deps.Host.CPUs)
	opts := make([]huh.Option[VMSize], len(sizes))
	for i, s := range sizes {
		opts[i] = huh.NewOption(s.String(), s)
	}
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Anthropic API key").
				Description("Use a dedicated workspace key with a spend limit. Setup stores it root-only for\n"+
					"the egress proxy; the review VM never sees it. Enter tests it against Anthropic.").
				EchoMode(huh.EchoModePassword).
				Value(&d.APIKey).
				Validate(keyValidator(deps.CheckKey)),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Budget per review (US$)").
				Description("A review stops when it reaches this. You can pass --budget to change it per run.").
				Value(&d.Budget).
				Validate(func(s string) error { _, err := ParseBudget(s); return err }),
			huh.NewInput().
				Title("Reports folder").
				Description("Each review writes report.md and summary.json into a new folder here.").
				Value(&d.ReportsDir).
				Validate(func(s string) error { _, err := ExpandReportsDir(s, deps.Home); return err }),
			huh.NewSelect[VMSize]().
				Title("Review VM size").
				Description("Sizes the host can spare; bigger repositories review faster with more.").
				Options(opts...).
				Value(&d.VMSize),
		),
	).WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithShowHelp(true)
}

// --- API key ---

// validateKeyFormat catches a mistyped or truncated key before any network
// call. Its errors never repeat the key.
func validateKeyFormat(key string) error {
	switch {
	case key == "":
		return errors.New("paste your API key")
	case strings.TrimSpace(key) != key || strings.ContainsAny(key, " \t\r\n"):
		return errors.New("the key holds spaces or line breaks; paste it again")
	case !strings.HasPrefix(key, "sk-ant-"):
		return errors.New("Anthropic API keys start with sk-ant-")
	case len(key) < 40 || len(key) > 256:
		return errors.New("the key looks cut short or too long; copy all of it")
	}
	for _, r := range key {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return errors.New("the key holds characters an API key never has")
		}
	}
	return nil
}

// keyValidator checks the format, then asks Anthropic. huh validates on
// Enter and again on blur, so it remembers its last answer and checks each
// key once.
func keyValidator(check func(context.Context, string) error) func(string) error {
	var mu sync.Mutex
	var lastKey string
	var lastErr error
	return func(key string) error {
		if err := validateKeyFormat(key); err != nil {
			return err
		}
		if check == nil {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if key == lastKey {
			return lastErr
		}
		err := check(context.Background(), key)
		// Remember only firm answers; a network failure or a busy server
		// gets another try on the next Enter.
		if err == nil || errors.Is(err, ErrKeyRejected) {
			lastKey, lastErr = key, err
		}
		return err
	}
}

// ErrKeyRejected marks a firm answer from Anthropic: the key itself got
// refused (401 or 403), so checking it again can't help.
var ErrKeyRejected = errors.New("Anthropic refused the key")

// KeyChecker tests a key by listing models, from the host (the VM never
// holds the key).
type KeyChecker struct {
	BaseURL string        // https://api.anthropic.com when empty
	Client  *http.Client  // http.DefaultClient when nil
	Timeout time.Duration // 10 seconds when zero
}

// Check implements FormDeps.CheckKey. Its errors never carry the key.
func (k KeyChecker) Check(ctx context.Context, key string) error {
	base, client, timeout := k.BaseURL, k.Client, k.Timeout
	if base == "" {
		base = "https://api.anthropic.com"
	}
	if client == nil {
		client = http.DefaultClient
	}
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models?limit=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("Anthropic didn't answer in time; check the network and try again")
		}
		return errors.New("couldn't reach Anthropic; check the network or proxy settings")
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (401): check you copied all of it and that nobody has revoked it", ErrKeyRejected)
	case http.StatusForbidden:
		return fmt.Errorf("%w (403): it lacks permission to list models; use a workspace key with API access", ErrKeyRejected)
	}
	return fmt.Errorf("Anthropic answered %d; try again shortly", resp.StatusCode)
}

// --- Budget, reports folder and VM size ---

// MaxBudget caps the budget a form accepts; --budget can't go past it either.
const MaxBudget = 1000

// ParseBudget reads a budget in US dollars, with or without a $ sign.
func ParseBudget(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "US"), "$")
	v, err := strconv.ParseFloat(s, 64)
	switch {
	case err != nil || math.IsNaN(v) || math.IsInf(v, 0):
		return 0, errors.New("enter an amount in US dollars, such as 25")
	case v <= 0:
		return 0, errors.New("the budget has to exceed zero")
	case v > MaxBudget:
		return 0, fmt.Errorf("budgets go up to US$%d", MaxBudget)
	}
	return v, nil
}

// systemDirs hold files the system owns; reports never go there.
var systemDirs = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var"}

// ExpandReportsDir turns ~ into home and checks the result: an absolute
// path outside the system's own folders.
func ExpandReportsDir(s, home string) (string, error) {
	s = strings.TrimSpace(s)
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
	for _, d := range systemDirs {
		if s == d || strings.HasPrefix(s, d+"/") {
			return "", fmt.Errorf("%s belongs to the system; pick a folder of your own", d)
		}
	}
	return s, nil
}

// VMSize sets a review VM's processors and memory.
type VMSize struct{ VCPUs, MemMiB int }

func (s VMSize) String() string {
	return fmt.Sprintf("%d vCPUs, %d GiB", s.VCPUs, s.MemMiB/1024)
}

// DefaultVMSize matches the shape cc-launch gives a review today.
var DefaultVMSize = VMSize{2, 2048}

var vmSizes = []VMSize{DefaultVMSize, {4, 4096}, {4, 8192}, {8, 16384}}

// VMSizes offers the sizes that leave the host at least half its memory and
// no fewer processors than the VM takes. The default always stays.
func VMSizes(ram uint64, cpus int) []VMSize {
	out := []VMSize{DefaultVMSize}
	for _, s := range vmSizes[1:] {
		if uint64(s.MemMiB)<<20 <= ram/2 && s.VCPUs <= cpus {
			out = append(out, s)
		}
	}
	return out
}
