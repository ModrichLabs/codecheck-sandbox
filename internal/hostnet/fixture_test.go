package hostnet

import (
	"os"
	"testing"
)

// fixture returns a real `nft -j list table inet codecheck` capture.
func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/nft-list.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
