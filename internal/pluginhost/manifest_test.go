package pluginhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/pluginapi"
)

func TestLoadDefinition(t *testing.T) {
	root := t.TempDir()
	entrypoint := filepath.Join(root, "plugin.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(root, ManifestFilename)
	writeManifest(t, manifestPath, pluginapi.Manifest{
		Name:            "telegram",
		Version:         "1.0.0",
		Kind:            pluginapi.KindChannel,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "plugin.sh",
	})

	def, err := LoadDefinition(manifestPath)
	if err != nil {
		t.Fatalf("LoadDefinition() error = %v", err)
	}
	if got := def.Entrypoint(); got != entrypoint {
		t.Fatalf("Entrypoint() = %q, want %q", got, entrypoint)
	}
}

func TestLoadDefinitionRejectsMissingEntrypoint(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, ManifestFilename)
	writeManifest(t, manifestPath, pluginapi.Manifest{
		Name:            "telegram",
		Version:         "1.0.0",
		Kind:            pluginapi.KindChannel,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "missing.sh",
	})

	if _, err := LoadDefinition(manifestPath); err == nil {
		t.Fatal("LoadDefinition() error = nil, want missing entrypoint error")
	}
}

func writeManifest(t *testing.T, path string, manifest pluginapi.Manifest) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
