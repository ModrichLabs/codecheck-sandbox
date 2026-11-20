package hostnet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// HashRules returns the SHA-256 of a canonical form of `nft -j list table`
// output: the metainfo object (nft's version) goes, every "handle" key goes
// at any depth, and keys get sorted. Rule order stays, since it decides what
// a packet meets first. cc-launch compares this hash before every VM.
func HashRules(nftJSON []byte) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(nftJSON, &doc); err != nil {
		return "", fmt.Errorf("reading nft JSON: %w", err)
	}
	items, ok := doc["nftables"].([]any)
	if !ok {
		return "", errors.New("reading nft JSON: no nftables list")
	}
	var canon []any
	for _, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			return "", errors.New("reading nft JSON: a list entry holds no object")
		}
		if _, meta := obj["metainfo"]; meta {
			continue
		}
		canon = append(canon, stripHandles(obj))
	}
	if len(canon) == 0 {
		return "", errors.New("reading nft JSON: no table")
	}
	// encoding/json writes map keys in sorted order.
	b, err := json.Marshal(canon)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func stripHandles(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k != "handle" {
				out[k] = stripHandles(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = stripHandles(val)
		}
		return out
	}
	return v
}

// LiveRuleHash hashes the loaded table, read as root with nft's JSON output.
func LiveRuleHash(ctx context.Context, r system.Runner, table string) (string, error) {
	res, err := r.Run(ctx, system.Cmd{Name: "nft", Args: []string{"-j", "list", "table", "inet", table}, Root: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("nft list table inet %s: %s", table, strings.TrimSpace(string(res.Stderr)))
	}
	return HashRules(res.Stdout)
}
