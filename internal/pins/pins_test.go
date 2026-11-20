package pins

import (
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var (
	sha256Hex  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitHex  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	fprHex     = regexp.MustCompile(`^[0-9A-F]{40}$`)
	integrity  = regexp.MustCompile(`^sha512-[A-Za-z0-9+/]{86}==$`)
	allPinSets = []any{Firecracker, Kernel, Ubuntu, ClaudeCode, CodeCheck}
)

// TestEveryDownloadPinHoldsAValidValue walks every pin struct: no field may
// stay empty, and each field's name decides the format it must have.
func TestEveryDownloadPinHoldsAValidValue(t *testing.T) {
	for _, set := range allPinSets {
		v := reflect.ValueOf(set)
		for i := range v.NumField() {
			name := v.Type().Name() + "." + v.Type().Field(i).Name
			s := v.Field(i).String()
			field := v.Type().Field(i).Name
			switch {
			case s == "":
				t.Errorf("%s stays empty", name)
			case strings.HasSuffix(field, "SHA256"):
				if !sha256Hex.MatchString(s) {
					t.Errorf("%s = %q, want 64 lowercase hex characters", name, s)
				}
			case field == "Commit":
				if !commitHex.MatchString(s) {
					t.Errorf("%s = %q, want 40 hex characters", name, s)
				}
			case field == "Integrity":
				if !integrity.MatchString(s) {
					t.Errorf("%s = %q, want an npm sha512- integrity", name, s)
				}
			case field == "KeyFingerprint":
				if !fprHex.MatchString(s) {
					t.Errorf("%s = %q, want a 40-character fingerprint", name, s)
				}
			case strings.HasSuffix(field, "URL"):
				if u, err := url.Parse(s); err != nil || u.Scheme != "https" || u.Host == "" {
					t.Errorf("%s = %q, want an https URL", name, s)
				}
			}
		}
	}
}

func TestPinsAgreeWithEachOther(t *testing.T) {
	if !strings.Contains(Firecracker.URL, Firecracker.Version) {
		t.Errorf("Firecracker URL %q does not name version %s", Firecracker.URL, Firecracker.Version)
	}
	if !strings.Contains(ClaudeCode.TarballURL, ClaudeCode.Version+".tgz") {
		t.Errorf("Claude Code tarball %q does not name version %s", ClaudeCode.TarballURL, ClaudeCode.Version)
	}
	base := Ubuntu.SquashFSURL[:strings.LastIndexByte(Ubuntu.SquashFSURL, '/')]
	if Ubuntu.SumsURL != base+"/SHA256SUMS" || Ubuntu.SumsSigURL != base+"/SHA256SUMS.gpg" {
		t.Errorf("Ubuntu checksum URLs must sit beside the image in %s", base)
	}
	if strings.Contains(Ubuntu.SquashFSURL, "/release/") {
		t.Errorf("Ubuntu image %q uses the moving release/ folder; pin a dated release-YYYYMMDD folder", Ubuntu.SquashFSURL)
	}
}

func TestPostHogPins(t *testing.T) {
	if !regexp.MustCompile(`^phc_[A-Za-z0-9]{20,}$`).MatchString(PostHogKey) {
		t.Errorf("PostHogKey %q is not a public phc_ project key", PostHogKey)
	}
	u, err := url.Parse(PostHogHost)
	if err != nil || u.Scheme != "https" || u.Host != "us.i.posthog.com" || u.Path != "" {
		t.Errorf("PostHogHost = %q, want https://us.i.posthog.com", PostHogHost)
	}
}
