package manifestplugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeMinimalManifest builds a Manifest with a single binary having explicit version
// so FetchLatestVersion is never called.
func makeMinimalManifest(pluginID string, enabled bool, binaryName, version string) *Manifest {
	return &Manifest{
		Plugins: []ManifestPlugin{
			{
				ID:      pluginID,
				Enabled: enabled,
				Binaries: []ManifestBinary{
					{
						Name:    binaryName,
						Tool:    "github:owner/repo",
						Version: version,
					},
				},
			},
		},
	}
}

// seedState writes a ManifestState JSON to the state file path so Reconcile
// treats the binary as already installed.
func seedState(t *testing.T, stellaHome, pluginID, binaryName, version string) {
	t.Helper()
	s := &ManifestState{
		UpdatedAt: time.Now(),
		Plugins: map[string]PluginInstallState{
			pluginID: {
				Binaries: []BinaryInstallState{
					{
						Name:        binaryName,
						Tool:        "github:owner/repo",
						Spec:        version,
						Version:     version,
						InstalledAt: time.Now(),
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stellaHome, "plugin-manifest-state.json"), data, 0o644); err != nil {
		t.Fatalf("write seed state: %v", err)
	}
}

// TestReconcile_CacheHit verifies that a binary already present in the state file
// is not re-downloaded and is reported as a cache hit.
func TestReconcile_CacheHit(t *testing.T) {
	stellaHome := t.TempDir()
	const (
		pluginID   = "test-plugin"
		binaryName = "mytool"
		version    = "1.2.3"
	)

	seedState(t, stellaHome, pluginID, binaryName, version)

	m := makeMinimalManifest(pluginID, true, binaryName, version)
	result := Reconcile(context.Background(), m, stellaHome)

	if result.EnabledCount != 1 {
		t.Errorf("EnabledCount = %d, want 1", result.EnabledCount)
	}

	pr, ok := result.Plugins[pluginID]
	if !ok {
		t.Fatalf("plugin %q not in result", pluginID)
	}

	if len(pr.Binaries) != 1 {
		t.Fatalf("len(Binaries) = %d, want 1", len(pr.Binaries))
	}

	br := pr.Binaries[0]
	if !br.CacheHit {
		t.Errorf("CacheHit = false, want true")
	}
	if br.Name != binaryName {
		t.Errorf("Name = %q, want %q", br.Name, binaryName)
	}
	if br.Version != version {
		t.Errorf("Version = %q, want %q", br.Version, version)
	}
	if br.Err != nil {
		t.Errorf("Err = %v, want nil", br.Err)
	}
}

// TestReconcile_DisabledPlugin verifies that disabled plugins are skipped entirely
// and do not appear in the result map.
func TestReconcile_XbergDoesNotReuseLegacyBinaryState(t *testing.T) {
	// Keep the historical plugin ID so persisted overrides and state remain in
	// place, but a prior executable must never satisfy Xberg's cache entry.
	const pluginID = "tool/kreuzberg"
	state := &ManifestState{Plugins: map[string]PluginInstallState{
		pluginID: {Binaries: []BinaryInstallState{{Name: "kreuzberg", Spec: "1.0.0-rc.28"}}},
	}}
	if isCacheHit(state, pluginID, "xberg", "1.0.0-rc.35") {
		t.Fatal("legacy binary state must not be an Xberg cache hit")
	}

	upsertBinaryState(state, pluginID, BinaryInstallState{Name: "xberg", Spec: "1.0.0-rc.35"})
	if !isCacheHit(state, pluginID, "xberg", "1.0.0-rc.35") {
		t.Fatal("Xberg state was not recorded under the stable plugin ID")
	}
}

func TestReconcile_DisabledPlugin(t *testing.T) {
	stellaHome := t.TempDir()
	const (
		pluginID   = "disabled-plugin"
		binaryName = "sometool"
		version    = "2.0.0"
	)

	m := makeMinimalManifest(pluginID, false, binaryName, version)
	result := Reconcile(context.Background(), m, stellaHome)

	if result.EnabledCount != 0 {
		t.Errorf("EnabledCount = %d, want 0", result.EnabledCount)
	}

	if _, ok := result.Plugins[pluginID]; ok {
		t.Errorf("disabled plugin %q should not appear in result.Plugins", pluginID)
	}
}

// TestReconcile_EnabledCount verifies that EnabledCount reflects only enabled plugins.
func TestReconcile_EnabledCount(t *testing.T) {
	stellaHome := t.TempDir()

	// Seed state for the enabled plugin so no download is attempted.
	seedState(t, stellaHome, "enabled-plugin", "tool-a", "0.1.0")

	m := &Manifest{
		Plugins: []ManifestPlugin{
			{
				ID:      "enabled-plugin",
				Enabled: true,
				Binaries: []ManifestBinary{
					{
						Name:    "tool-a",
						Tool:    "github:owner/tool-a",
						Version: "0.1.0",
					},
				},
			},
			{
				ID:      "disabled-plugin-1",
				Enabled: false,
				Binaries: []ManifestBinary{
					{
						Name:    "tool-b",
						Tool:    "github:owner/tool-b",
						Version: "1.0.0",
					},
				},
			},
			{
				ID:      "disabled-plugin-2",
				Enabled: false,
				Binaries: []ManifestBinary{
					{
						Name:    "tool-c",
						Tool:    "github:owner/tool-c",
						Version: "3.0.0",
					},
				},
			},
		},
	}

	result := Reconcile(context.Background(), m, stellaHome)

	if result.EnabledCount != 1 {
		t.Errorf("EnabledCount = %d, want 1", result.EnabledCount)
	}

	if _, ok := result.Plugins["enabled-plugin"]; !ok {
		t.Error("enabled-plugin should appear in result.Plugins")
	}
	if _, ok := result.Plugins["disabled-plugin-1"]; ok {
		t.Error("disabled-plugin-1 should not appear in result.Plugins")
	}
	if _, ok := result.Plugins["disabled-plugin-2"]; ok {
		t.Error("disabled-plugin-2 should not appear in result.Plugins")
	}
}

// TestLoadState_MissingFile verifies that LoadState returns an empty state when the file doesn't exist.
func TestLoadState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent-state.json")
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState on missing file = %v, want nil error", err)
	}
	if s == nil {
		t.Fatal("LoadState returned nil state")
	}
	if len(s.Plugins) != 0 {
		t.Errorf("Plugins = %v, want empty map", s.Plugins)
	}
}

// TestSaveAndLoadState verifies round-trip correctness for SaveState/LoadState.
func TestSaveAndLoadState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	now := time.Now().Truncate(time.Second)
	original := &ManifestState{
		UpdatedAt: now,
		Plugins: map[string]PluginInstallState{
			"my-plugin": {
				Binaries: []BinaryInstallState{
					{Name: "my-tool", Tool: "github:owner/my-tool", Version: "1.0.0", InstalledAt: now},
				},
			},
		},
	}

	if err := SaveState(path, original); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	ps, ok := loaded.Plugins["my-plugin"]
	if !ok {
		t.Fatal("my-plugin missing from loaded state")
	}
	if len(ps.Binaries) != 1 {
		t.Fatalf("len(Binaries) = %d, want 1", len(ps.Binaries))
	}
	b := ps.Binaries[0]
	if b.Name != "my-tool" || b.Version != "1.0.0" {
		t.Errorf("binary = %+v, unexpected values", b)
	}
}

// TestStatePath verifies the helper returns the expected path.
func TestStatePath(t *testing.T) {
	want := filepath.Join("/home/user/.stella", "plugin-manifest-state.json")
	got := StatePath("/home/user/.stella")
	if got != want {
		t.Errorf("StatePath = %q, want %q", got, want)
	}
}
