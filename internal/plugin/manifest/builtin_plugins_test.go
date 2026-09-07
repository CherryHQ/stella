package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var testReservedRuntimeNames = []string{"mise", "xberg"}

const testBuiltinPluginJSON = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","description":"A demo plugin","extensions":{"com.cherryhq.stella":{"version":"1","binaries":[{"name":"demo","tool":"demo"}]}}}`

func TestGenerateBuiltinPluginsRootMovePreservesBytesAndIdentity(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "first")
	writeBuiltinPlugin(t, root, "agent/demo/plugin.json", testBuiltinPluginJSON)
	first, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := renderBuiltinPlugins(first)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "second")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	second, err := GenerateBuiltinPlugins(moved, testReservedRuntimeNames, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := renderBuiltinPlugins(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) || second.Plugins[0].ID != "demo" {
		t.Fatal("root move changed catalog")
	}
}

func TestGenerateBuiltinPluginsEmptyDirectoryIsNotPlugin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinPlugin(t, root, "agent/demo/plugin.json", testBuiltinPluginJSON)
	result, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil)
	if err != nil || len(result.Plugins) != 1 {
		t.Fatalf("catalog = %+v, error = %v", result, err)
	}
}

func TestGenerateBuiltinPluginsRejectsDuplicateIDsAndResources(t *testing.T) {
	for _, duplicateID := range []bool{true, false} {
		first := ManifestPlugin{ID: "demo", ManifestPluginDefinition: ManifestPluginDefinition{Name: "demo", DisplayName: "Demo", SessionEnvs: []ManifestSessionEnv{{EnvVar: "TOKEN", Source: "oauth.access_token"}}, OAuthProvider: "demo"}}
		second := first
		want := "duplicate builtin plugin ID"
		if !duplicateID {
			second.ID = "other"
			second.Name = "other"
			want = "duplicate builtin plugin resource"
		}
		if err := validateBuiltinPlugins([]ManifestPlugin{first, second}, testReservedRuntimeNames, []string{"demo"}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error=%v, want %s", err, want)
		}
	}
}

func TestGenerateBuiltinPluginsRejectsSymlinkAndNonRegularManifest(t *testing.T) {
	if runtime.GOOS != "windows" {
		root := t.TempDir()
		writeBuiltinPlugin(t, root, "agent/demo/plugin.json", testBuiltinPluginJSON)
		if err := os.Symlink(filepath.Join(root, "agent/demo"), filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error=%v, want symlink rejection", err)
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bad", "plugin.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil); err == nil || !strings.Contains(err.Error(), "unsupported type directory") {
		t.Fatalf("error=%v, want nonregular rejection", err)
	}
}

func TestWriteBuiltinPluginsRefreshesGeneratedBytes(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "plugins")
	output := filepath.Join(root, "generated.go")
	writeBuiltinPlugin(t, pluginsRoot, "agent/demo/plugin.json", testBuiltinPluginJSON)
	if err := WriteBuiltinPlugins(pluginsRoot, output, testReservedRuntimeNames, nil); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	writeBuiltinPlugin(t, pluginsRoot, "agent/demo/plugin.json", strings.Replace(testBuiltinPluginJSON, "A demo plugin", "Updated demo plugin", 1))
	if err := WriteBuiltinPlugins(pluginsRoot, output, testReservedRuntimeNames, nil); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || !bytes.Contains(second, []byte("Updated demo plugin")) {
		t.Fatal("generated output did not refresh")
	}
}

func TestGenerateBuiltinPluginsRejectsInvalidAuthoring(t *testing.T) {
	for _, tc := range []struct{ name, filename, content string }{
		{"legacy plugin", "tools/demo/plugin.yaml", "id: demo"},
		{"legacy assets", "tools/demo/assets.yaml", "assets: []"},
		{"unknown field", "agent/demo/plugin.json", strings.TrimSuffix(testBuiltinPluginJSON, "}") + `,"unexpected":true}`},
		{"multiple documents", "agent/demo/plugin.json", testBuiltinPluginJSON + "{}"},
		{"remote skill source", "agent/demo/plugin.json", strings.TrimSuffix(testBuiltinPluginJSON, "}") + `,"skills":[{"name":"demo","repo":"github:example/demo"}]}`},
		{"essential", "agent/demo/plugin.json", strings.TrimSuffix(testBuiltinPluginJSON, "}") + `,"essential":false}`},
		{"bundled binaries", "agent/demo/plugin.json", strings.TrimSuffix(testBuiltinPluginJSON, "}") + `,"bundled_binaries":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuiltinPlugin(t, root, tc.filename, tc.content)
			if _, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil); err == nil {
				t.Fatal("invalid authoring accepted")
			}
		})
	}
}

func TestGenerateBuiltinPluginsRejectsReservedCoreBinaryNames(t *testing.T) {
	for _, name := range testReservedRuntimeNames {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			content := strings.Replace(testBuiltinPluginJSON, `"binaries":[{"name":"demo"`, `"binaries":[{"name":"`+name+`"`, 1)
			writeBuiltinPlugin(t, root, "agent/demo/plugin.json", content)
			if _, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil); err == nil || !strings.Contains(err.Error(), "reserved core binary name") {
				t.Fatalf("error=%v, want reserved name rejection", err)
			}
		})
	}
}

func TestGenerateBuiltinPluginsIncludesStandardAgentPackage(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "agent", "demo")
	if err := os.MkdirAll(filepath.Join(packageRoot, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "plugin.json"), []byte(`{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "1.0.0",
  "description": "Demo Agent package"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "skills", "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: Demo skill\n---\n\nUse the demo skill.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "skills", "demo", "plugin.json"), []byte(`{"name":"skill-attachment"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plugins, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins.Plugins) != 1 {
		t.Fatalf("plugins = %#v, want one Agent package", plugins.Plugins)
	}
	got := plugins.Plugins[0]
	if got.ID != "demo" || got.Name != "demo" || len(got.Skills) != 1 || got.Skills[0].Name != "demo" {
		t.Fatalf("Agent plugin = %#v", got)
	}
}

func writeBuiltinPlugin(t *testing.T, root, relative, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateBuiltinPluginsSharesOnlyIdenticalCLIRequirements(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ManifestBinary)
	}{
		{name: "identical"},
		{name: "source", change: func(b *ManifestBinary) { b.Tool = "npm:bun" }},
		{name: "version", change: func(b *ManifestBinary) { b.Version = "2" }},
		{name: "options", change: func(b *ManifestBinary) { b.Options = map[string]any{"bin_path": "other"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"bun", "web"} {
				binary := ManifestBinary{Name: "bun", Tool: "bun", Version: "1", Options: map[string]any{"bin_path": "bin"}}
				if name == "web" && test.change != nil {
					test.change(&binary)
				}
				descriptor, err := json.Marshal(map[string]any{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": name, "extensions": map[string]any{"com.cherryhq.stella": map[string]any{"version": "1", "binaries": []ManifestBinary{binary}}}})
				if err != nil {
					t.Fatal(err)
				}
				writeBuiltinPlugin(t, root, "agent/"+name+"/plugin.json", string(descriptor))
			}
			catalog, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, nil)
			if test.change == nil {
				if err != nil {
					t.Fatal(err)
				}
				if len(catalog.Plugins) != 2 {
					t.Fatalf("got %d packages, want both owners", len(catalog.Plugins))
				}
			} else if err == nil || !strings.Contains(err.Error(), "conflicting builtin plugin resource binary") {
				t.Fatalf("got %v, want conflicting binary rejection", err)
			}
		})
	}
}
