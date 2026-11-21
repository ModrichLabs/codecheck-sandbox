package hostnet

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
)

// cc-launch can't import hostnet (it would pull in telemetry), so it repeats
// a few values; this test keeps both sides in step.
func TestLaunchMatchesTheHostConfig(t *testing.T) {
	c, p := DefaultConfig(), launch.DefaultPaths()
	if want := net.JoinHostPort(c.HostIP, strconv.Itoa(c.ProxyPort)); launch.ProxyAddr != want {
		t.Errorf("launch.ProxyAddr = %s, want %s", launch.ProxyAddr, want)
	}
	if !strings.HasSuffix(c.ProxyUnitPath, "/"+launch.ProxyUnit) {
		t.Errorf("launch.ProxyUnit = %s, but the unit lives at %s", launch.ProxyUnit, c.ProxyUnitPath)
	}
	if launch.RulesTable != c.Table {
		t.Errorf("launch.RulesTable = %s, want %s", launch.RulesTable, c.Table)
	}
	if unit := rendered(t, "codecheck-proxy.service"); !strings.Contains(unit, "--deny-log "+p.DenyLog) {
		t.Errorf("the proxy unit doesn't write its denials to %s, where cc-launch reads them", p.DenyLog)
	}
	if p.RulesHash != c.HashPath || p.JailerBase != c.JailerBase {
		t.Errorf("launch paths %s, %s; hostnet %s, %s", p.RulesHash, p.JailerBase, c.HashPath, c.JailerBase)
	}
}
