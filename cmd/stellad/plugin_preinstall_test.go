package main

import (
	"os"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestPreinstallationSkipsDisabledPackages(t *testing.T) {
	home := t.TempDir()
	definitions := []plugin.Definition{{ID: "disabled", DefaultEnabled: false, Spec: []byte(`{"binaries":[{"name":"unused","tool":"github:owner/repo","version":"1"}]}`)}}
	if err := warmAgentPackageArtifacts(t.Context(), home, definitions); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("disabled prewarm created installation state: %v, %v", entries, err)
	}
}
