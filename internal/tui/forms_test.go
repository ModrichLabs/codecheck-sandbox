package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// goodKey has a real key's shape; it gets built at run time so the source
// never holds anything a secret scanner would flag.
var goodKey = "sk-ant-" + "api03-" + strings.Repeat("Ab3_", 23) + "x" + "AA"

func TestKeyFormat(t *testing.T) {
	if err := validateKeyFormat(goodKey); err != nil {
		t.Errorf("a well-formed key: %v", err)
	}
	for _, bad := range []string{"", "sk-proj-abc", "sk-ant-api03-short", goodKey + " ", "sk-ant-api03-" + strings.Repeat("x", 80) + "\n"} {
		err := validateKeyFormat(bad)
		if err == nil {
			t.Errorf("accepted %q", bad)
			continue
		}
		if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("the error repeats the key: %v", err)
		}
	}
}

func TestCheckKeyAgainstTheModelsEndpoint(t *testing.T) {
	for _, c := range []struct {
		status int
		want   string
	}{
		{200, ""},
		{401, "refused"},
		{403, "permission"},
		{429, "429"},
		{500, "500"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != goodKey || r.Header.Get("anthropic-version") == "" {
				t.Errorf("request %s %v", r.URL.Path, r.Header)
			}
			w.WriteHeader(c.status)
		}))
		err := KeyChecker{BaseURL: srv.URL, Client: srv.Client()}.Check(context.Background(), goodKey)
		srv.Close()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%d: %v", c.status, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%d: %v, want it to mention %q", c.status, err, c.want)
		case err != nil && strings.Contains(err.Error(), goodKey):
			t.Errorf("%d: the error repeats the key", c.status)
		}
	}
}

func TestCheckKeyGivesUpInTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	start := time.Now()
	err := KeyChecker{BaseURL: srv.URL, Client: srv.Client(), Timeout: 50 * time.Millisecond}.Check(context.Background(), goodKey)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err %v after %v", err, time.Since(start))
	}
	if strings.Contains(err.Error(), goodKey) {
		t.Error("the error repeats the key")
	}
}

// huh validates on Enter and again on blur; the key check must hit the
// network once per key.
func TestKeyValidatorRemembersItsLastAnswer(t *testing.T) {
	var calls atomic.Int32
	v := keyValidator(func(ctx context.Context, key string) error {
		calls.Add(1)
		if key == goodKey {
			return nil
		}
		return errors.New("rejected")
	})
	for range 3 {
		if err := v(goodKey); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d checks for one key", calls.Load())
	}
	if v("sk-ant-api03-"+strings.Repeat("z", 95)) == nil || calls.Load() != 2 {
		t.Error("a new key didn't get checked")
	}
	if v("nonsense") == nil || calls.Load() != 2 {
		t.Error("a malformed key reached the network")
	}
}

func TestBudget(t *testing.T) {
	for in, want := range map[string]float64{"25": 25, "1": 1, "7.50": 7.5, " 10 ": 10, "$12": 12, "US$5": 5} {
		got, err := ParseBudget(in)
		if err != nil || got != want {
			t.Errorf("ParseBudget(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-5", "abc", "1e9", "NaN", "Inf", "1001"} {
		if _, err := ParseBudget(bad); err == nil {
			t.Errorf("ParseBudget(%q) passed", bad)
		}
	}
}

func TestReportsFolder(t *testing.T) {
	home := t.TempDir()
	for in, want := range map[string]string{
		"~/codecheck-reports": home + "/codecheck-reports",
		home + "/r":           home + "/r",
		"~":                   home,
	} {
		got, err := ExpandReportsDir(in, home)
		if err != nil || got != want {
			t.Errorf("ExpandReportsDir(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "reports", "~other/x", "/etc/reports", "/", "/usr/share/x", "/proc/x"} {
		if _, err := ExpandReportsDir(bad, home); err == nil {
			t.Errorf("ExpandReportsDir(%q) passed", bad)
		}
	}
}

func TestDetailsFormBuilds(t *testing.T) {
	d := Details{Budget: "25", ReportsDir: "~/codecheck-reports"}
	f := DetailsForm(&d, FormDeps{Home: t.TempDir()})
	if f == nil {
		t.Fatal("no form")
	}
	f.Init()
	f.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if v := stripANSI(f.View()); !strings.Contains(v, "API key") {
		t.Errorf("the first page lacks the key field:\n%s", v)
	}
}
