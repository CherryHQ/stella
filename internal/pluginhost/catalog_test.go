package pluginhost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/pluginapi"
)

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "tool", "plugin.json"), pluginapi.Manifest{
		Name:            "read",
		Version:         "1.0.0",
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "plugin.sh",
		Tool: &pluginapi.ToolSpec{
			Name:        "read",
			Description: "read",
			InputSchema: map[string]any{},
		},
	})
	if err := os.WriteFile(filepath.Join(root, "tool", "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	catalog, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := len(catalog.List()); got != 1 {
		t.Fatalf("len(List()) = %d, want 1", got)
	}
	if _, ok := catalog.Get("tool/read"); !ok {
		t.Fatalf("expected tool/read in catalog")
	}
}

func TestDiscoverDuplicate(t *testing.T) {
	root := t.TempDir()

	writeManifest(t, filepath.Join(root, "a", "plugin.json"), pluginapi.Manifest{
		Name:            "read",
		Version:         "1.0.0",
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "plugin.sh",
		Tool: &pluginapi.ToolSpec{
			Name:        "read",
			Description: "read",
			InputSchema: map[string]any{},
		},
	})
	writeManifest(t, filepath.Join(root, "b", "plugin.json"), pluginapi.Manifest{
		Name:            "read",
		Version:         "1.0.1",
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "plugin.sh",
		Tool: &pluginapi.ToolSpec{
			Name:        "read",
			Description: "read",
			InputSchema: map[string]any{},
		},
	})

	if err := os.WriteFile(filepath.Join(root, "a", "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b", "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Discover(root); err == nil {
		t.Fatal("Discover() error = nil, want duplicate error")
	}
}
