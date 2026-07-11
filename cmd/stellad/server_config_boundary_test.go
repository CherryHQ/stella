package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerConfigLoadedOnlyAtServerBoundary is a structural guard for issue
// #701's hard requirement: parsing the full ServerConfig must happen only at the
// server startup boundary, so operator commands that never start the server
// (version, vault keygen, service, mise, upgrade, postgres) cannot be blocked by
// an unrelated bad variable. It asserts the source references to
// LoadServerConfig live only in gateway.go (serverAction).
//
// This makes the boundary provable at the file level; the CLI smoke test
// (`stellad version` with a garbage duration exiting 0) proves the runtime
// behavior it implies.
func TestServerConfigLoadedOnlyAtServerBoundary(t *testing.T) {
	const allowedFile = "gateway.go"
	symbols := []string{"config.LoadServerConfig"}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/stellad: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == allowedFile {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, sym := range symbols {
			if strings.Contains(string(src), sym) {
				t.Errorf("%s references %s; server config must load only in %s (serverAction)", name, sym, allowedFile)
			}
		}
	}
}
