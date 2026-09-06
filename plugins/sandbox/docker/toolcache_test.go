package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectionMiseTOMLRegistryTool(t *testing.T) {
	got, err := selectionMiseTOML([]ToolBinary{{Name: "uv", Tool: "uv"}})
	if err != nil {
		t.Fatalf("selectionMiseTOML: %v", err)
	}
	want := `uv = 'latest'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected registry tool form %q in:\n%s", want, got)
	}
}

func TestSelectionMiseTOMLRejectsConflictingScopes(t *testing.T) {
	_, err := selectionMiseTOML([]ToolBinary{
		{PluginID: "tool/uv", ConfigID: "system", Scope: "system", Name: "uv", Tool: "uv", Version: "1"},
		{PluginID: "tool/uv", ConfigID: "user", Scope: "user", Name: "uv", Tool: "uv", Version: "2"},
	})
	if err == nil || !strings.Contains(err.Error(), `disagree on mise tool "uv"`) {
		t.Fatalf("selectionMiseTOML error = %v, want conflicting tool error", err)
	}
}

func TestSelectionToolCacheScriptPublishesOnlySelectedBundledAlias(t *testing.T) {
	bundled := []ToolBinary{{PluginID: "tool/xberg", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "xberg"}}
	script := selectionToolInstallScript("hash", nil, bundled)
	if !strings.Contains(script, "test -x /opt/stella/bin/xberg") || !strings.Contains(script, "/opt/stella/selection-tools/artifacts/bundled-xberg/xberg") {
		t.Fatalf("selection helper did not publish trusted xberg alias:\n%s", script)
	}
	if strings.Contains(script, "ln -s /opt/stella/bin/mise") {
		t.Fatalf("selection helper exposed unselected mise:\n%s", script)
	}
}

func TestSelectionToolInstallScriptCoreOnlyDoesNotExposeMise(t *testing.T) {
	script := selectionToolInstallScript("hash", nil, nil)
	if strings.Contains(script, `cp -a /opt/stella/bin/mise "$ROOT/bin/mise"`) {
		t.Fatal("core-only selection must not expose the image mise binary")
	}
	if strings.Contains(script, "/opt/stella/bin/mise install") || strings.Contains(script, "STELLA_SELECTION_MISE_TOML") {
		t.Fatal("core-only selection must not create or run a private mise install")
	}
}

func TestSelectionToolCacheHashIncludesIdentityAndRevision(t *testing.T) {
	base := []ToolBinary{{PluginID: "tool/one", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "one", Tool: "github:owner/one", Version: "1"}}
	other := []ToolBinary{{PluginID: "tool/one", ConfigID: "cfg", Scope: "system", Revision: 2, Name: "one", Tool: "github:owner/one", Version: "1"}}
	bundled := []ToolBinary{{PluginID: "tool/xberg", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "xberg"}}
	if selectionToolCacheHash("sha256:image-a", base, bundled) == selectionToolCacheHash("sha256:image-a", other, bundled) {
		t.Fatal("selection cache identity must include config revision")
	}
	if selectionToolCacheHash("sha256:image-a", base, bundled) == selectionToolCacheHash("sha256:image-b", base, bundled) {
		t.Fatal("selection cache identity must include resolved image ID")
	}
	second := ToolBinary{PluginID: "tool/two", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "two", Tool: "uv", Version: "2"}
	if selectionToolCacheHash("sha256:image-a", []ToolBinary{base[0], second}, nil) != selectionToolCacheHash("sha256:image-a", []ToolBinary{second, base[0]}, nil) {
		t.Fatal("selection cache hash must be independent of input order")
	}
}

func TestSelectionToolInstallScriptRemovesPrivateInstallerState(t *testing.T) {
	bundled := []ToolBinary{{PluginID: "tool/xberg", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "xberg"}}
	script := selectionToolInstallScript("hash", []ToolBinary{{Name: "uv", Tool: "uv"}}, bundled)
	for _, required := range []string{"PRIVATE=/tmp/stella-selection-private", "trap 'rm -rf \"$PRIVATE\"'", "cp -R \"$install_dir/.\"", "readlink -f /opt/stella/bin/xberg"} {
		if !strings.Contains(script, required) {
			t.Fatalf("selection script missing %q:\n%s", required, script)
		}
	}
	for _, required := range []string{"MISE_CACHE_DIR=\"$PRIVATE/mise-cache\"", "MISE_STATE_DIR=\"$PRIVATE/mise-state\"", "MISE_CONFIG_DIR=\"$PRIVATE/mise-config\"", "MISE_SYSTEM_CONFIG_FILE=\"$PRIVATE/mise.toml\""} {
		if !strings.Contains(script, required) {
			t.Fatalf("selection script does not isolate mise path %q:\n%s", required, script)
		}
	}
	if strings.Contains(script, "MISE_SYSTEM_CONFIG_FILE=/opt/stella") || strings.Contains(script, "ln -s /opt/stella/bin/xberg") {
		t.Fatalf("selection script leaks shared installer state or image alias:\n%s", script)
	}
}

func TestSelectionToolInstallScriptPublishesRunnableArtifactsAndNoPrivateState(t *testing.T) {
	bundled := []ToolBinary{{PluginID: "tool/xberg", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "xberg"}}
	imageBin := filepath.Join(t.TempDir(), "image-bin")
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	if err := os.MkdirAll(imageBin, 0o755); err != nil {
		t.Fatal(err)
	}
	mise := `#!/bin/sh
set -eu
case "$1" in
trust) exit 0 ;;
install) mkdir -p "$MISE_DATA_DIR/installs/uv/1/bin"; printf '#!/bin/sh\nprintf "uv-ok\\n"\n' > "$MISE_DATA_DIR/installs/uv/1/bin/uv"; chmod 755 "$MISE_DATA_DIR/installs/uv/1/bin/uv" ;;
where) printf '%s/installs/uv/1\n' "$MISE_DATA_DIR" ;;
esac
`
	if err := os.WriteFile(filepath.Join(imageBin, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(imageBin, "xberg-v1")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "xberg"), []byte("#!/bin/sh\nprintf 'xberg-ok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "libxberg.so"), []byte("sidecar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(bundleDir, "xberg"), filepath.Join(imageBin, "xberg")); err != nil {
		t.Fatal(err)
	}

	script := selectionToolInstallScript("hash", []ToolBinary{{Name: "uv", Tool: "uv", Version: "1"}}, bundled)
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/bin", imageBin)
	cmd := exec.Command("/bin/sh", "-c", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("selection installer: %v\n%s\nscript:\n%s", err, output, script)
	}
	for _, private := range []string{"mise.toml", "mise-data"} {
		if _, err := os.Stat(filepath.Join(selectionRoot, private)); !os.IsNotExist(err) {
			t.Fatalf("private installer state %s remains, err=%v", private, err)
		}
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "artifacts", "artifact-uv", "bin", "uv")); err != nil {
		t.Fatalf("selected runtime artifact missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "artifacts", "bundled-xberg", "libxberg.so")); err != nil {
		t.Fatalf("bundled sidecar missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "bin", "mise")); !os.IsNotExist(err) {
		t.Fatalf("core mise must stay hidden unless selected, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "bin", "unselected")); !os.IsNotExist(err) {
		t.Fatalf("unselected image binary was published, err=%v", err)
	}
	pathEnv := filepath.Join(selectionRoot, "bin")
	result := exec.Command("/bin/sh", "-c", "uv && xberg")
	result.Env = append(os.Environ(), "PATH="+pathEnv)
	output, err := result.CombinedOutput()
	if err != nil || string(output) != "uv-ok\nxberg-ok\n" {
		t.Fatalf("selected aliases output=%q err=%v", output, err)
	}
}
