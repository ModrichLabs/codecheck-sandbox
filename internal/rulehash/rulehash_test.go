package rulehash

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/nft-list.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rewrite decodes the fixture, applies f, and encodes it again.
func rewrite(t *testing.T, f func(items []any) []any) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture(t), &doc); err != nil {
		t.Fatal(err)
	}
	doc["nftables"] = f(doc["nftables"].([]any))
	b, _ := json.Marshal(doc)
	return b
}

func TestRuleHashStable(t *testing.T) {
	a, err := Hash(fixture(t))
	if err != nil {
		t.Fatalf("HashRules: %v", err)
	}
	b, _ := Hash(fixture(t))
	if a != b || len(a) != 64 {
		t.Errorf("hashes %s / %s, want one stable 64-hex value", a, b)
	}
}

func TestRuleHashIgnoresHandlesAndVersion(t *testing.T) {
	want, _ := Hash(fixture(t))
	shifted := rewrite(t, func(items []any) []any {
		for _, it := range items {
			for _, v := range it.(map[string]any) {
				if obj, ok := v.(map[string]any); ok {
					if h, ok := obj["handle"].(float64); ok {
						obj["handle"] = h + 40
					}
					if _, ok := obj["version"]; ok {
						obj["version"] = "1.0.6"
					}
				}
			}
		}
		return items
	})
	if got, _ := Hash(shifted); got != want {
		t.Errorf("new handles or another nft version changed the hash")
	}
}

func TestRuleHashSeesAnExtraRule(t *testing.T) {
	want, _ := Hash(fixture(t))
	extra := rewrite(t, func(items []any) []any {
		return append(items, map[string]any{"rule": map[string]any{
			"family": "inet", "table": "codecheck", "chain": "input",
			"expr": []any{map[string]any{"accept": nil}},
		}})
	})
	if got, _ := Hash(extra); got == want {
		t.Error("an extra accept rule left the hash unchanged")
	}
}

func TestRuleHashSeesReorderedRules(t *testing.T) {
	want, _ := Hash(fixture(t))
	swapped := rewrite(t, func(items []any) []any {
		var rules []int
		for i, it := range items {
			if _, ok := it.(map[string]any)["rule"]; ok {
				rules = append(rules, i)
			}
		}
		items[rules[0]], items[rules[1]] = items[rules[1]], items[rules[0]]
		return items
	})
	if got, _ := Hash(swapped); got == want {
		t.Error("moving the drop above the accept left the hash unchanged")
	}
}

func TestRuleHashRefusesGarbage(t *testing.T) {
	for _, in := range []string{"", "{}", `{"nftables": 3}`, "not json"} {
		if _, err := Hash([]byte(in)); err == nil {
			t.Errorf("Hash(%q) succeeded", in)
		}
	}
}

func TestLiveSkipsThePrivilegeToolWhenAlreadyRoot(t *testing.T) {
	// cc-launch runs as root already; a Root command would go nowhere.
	f := &system.FakeRunner{}
	f.On("nft").Return(system.Result{Stdout: fixture(t)})
	if _, err := Live(context.Background(), f, "codecheck", false); err != nil {
		t.Fatalf("Live: %v", err)
	}
	if f.Calls()[0].Root {
		t.Error("Live asked for the privilege tool with root=false")
	}
}

func TestLiveRuleHashRunsNftAsRoot(t *testing.T) {
	f := &system.FakeRunner{}
	f.On("nft").Return(system.Result{Stdout: fixture(t)})
	got, err := Live(context.Background(), f, "codecheck", true)
	if err != nil {
		t.Fatalf("LiveRuleHash: %v", err)
	}
	want, _ := Hash(fixture(t))
	if got != want {
		t.Errorf("LiveRuleHash = %s, want %s", got, want)
	}
	c := f.Calls()[0]
	if !c.Root || !reflect.DeepEqual(c.Args, []string{"-j", "list", "table", "inet", "codecheck"}) {
		t.Errorf("call = %+v, want nft -j list table inet codecheck as root", c)
	}

	f = &system.FakeRunner{}
	f.On("nft").Return(system.Result{Code: 1, Stderr: []byte("Error: No such file or directory")})
	if _, err := Live(context.Background(), f, "codecheck", true); err == nil || !strings.Contains(err.Error(), "No such file") {
		t.Errorf("err = %v, want nft's error when the table is missing", err)
	}
}
