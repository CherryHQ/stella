package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// makeMinimalManifest declares one binary for installer tests.
func makeMinimalManifest(pluginID string, enabled bool, binaryName, version string) *Manifest {
	return &Manifest{
		Plugins: []ManifestPlugin{
			{
				ID:      pluginID,
				Enabled: enabled,
				ManifestPluginDefinition: ManifestPluginDefinition{Binaries: []ManifestBinary{
					{
						Name:    binaryName,
						Tool:    "github:owner/repo",
						Version: version,
					},
				}},
			},
		},
	}
}

func TestWarmBuiltinArtifactsSkipsDisabledPackages(t *testing.T) {
	home := t.TempDir()
	if err := WarmBuiltinArtifacts(t.Context(), makeMinimalManifest("disabled", false, "unused", "1"), home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("disabled prewarm created installation state: %v, %v", entries, err)
	}
}

func assertNoPrewarmPublication(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{"plugin-manifest-state.json", ".mise-tools/configs/_builtin.toml", ".mise-tools/public"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("prewarm published %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".mise-private"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("private install inputs survived: %v, %v", entries, err)
	}
}
