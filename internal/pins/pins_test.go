package pins

import (
	"net/url"
	"regexp"
	"testing"
)

func TestPostHogPins(t *testing.T) {
	if !regexp.MustCompile(`^phc_[A-Za-z0-9]{20,}$`).MatchString(PostHogKey) {
		t.Errorf("PostHogKey %q is not a public phc_ project key", PostHogKey)
	}
	u, err := url.Parse(PostHogHost)
	if err != nil || u.Scheme != "https" || u.Host != "us.i.posthog.com" || u.Path != "" {
		t.Errorf("PostHogHost = %q, want https://us.i.posthog.com", PostHogHost)
	}
}
