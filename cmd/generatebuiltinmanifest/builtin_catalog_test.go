package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/resources/binaries"
)

var testReservedRuntimeNames = []string{"mise", "xberg"}

const testBuiltinPluginJSON = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","description":"A demo plugin","extensions":{"com.cherryhq.stella":{"version":"1","binaries":[{"name":"demo","tool":"demo"}]}}}`

func TestGenerateBuiltinDefinitionsRootMovePreservesBytesAndIdentity(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "first")
	writeBuiltinPlugin(t, root, "agent/demo/plugin.json", testBuiltinPluginJSON)
	first, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := renderBuiltinDefinitions(first)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "second")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	second, err := generateBuiltinDefinitions(moved, testReservedRuntimeNames, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := renderBuiltinDefinitions(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) || second[0].ID != "demo" {
		t.Fatal("root move changed catalog")
	}
}

func TestGenerateBuiltinDefinitionsEmptyDirectoryIsNotPlugin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinPlugin(t, root, "agent/demo/plugin.json", testBuiltinPluginJSON)
	result, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil)
	if err != nil || len(result) != 1 {
		t.Fatalf("catalog = %+v, error = %v", result, err)
	}
}

func TestGenerateBuiltinDefinitionsRejectsDuplicateIDsAndResources(t *testing.T) {
	first := makeBuiltinDefinition(t, "demo", plugin.ResourcePayload{
		SessionEnvs: []plugin.SessionEnvResource{{EnvVar: "TOKEN", Source: "oauth.access_token"}}, OAuthProvider: "demo",
	})
	second := first
	if err := validateBuiltinDefinitions([]builtinDefinition{first, second}, testReservedRuntimeNames, map[string]struct{}{"demo": {}}); err == nil || !strings.Contains(err.Error(), "duplicate builtin plugin ID") {
		t.Fatalf("error=%v, want duplicate builtin plugin ID", err)
	}
	second.ID = "other"
	if err := validateBuiltinDefinitions([]builtinDefinition{first, second}, testReservedRuntimeNames, map[string]struct{}{"demo": {}}); err == nil || !strings.Contains(err.Error(), "duplicate builtin plugin resource") {
		t.Fatalf("error=%v, want duplicate builtin plugin resource", err)
	}
}

func TestGenerateBuiltinDefinitionsRejectsSymlinkAndNonRegularManifest(t *testing.T) {
	if runtime.GOOS != "windows" {
		root := t.TempDir()
		writeBuiltinPlugin(t, root, "agent/demo/plugin.json", testBuiltinPluginJSON)
		if err := os.Symlink(filepath.Join(root, "agent/demo"), filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error=%v, want symlink rejection", err)
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bad", "plugin.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil); err == nil || !strings.Contains(err.Error(), "unsupported type directory") {
		t.Fatalf("error=%v, want nonregular rejection", err)
	}
}

func TestWriteBuiltinDefinitionsRefreshesGeneratedBytes(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "plugins")
	output := filepath.Join(root, "generated.go")
	writeBuiltinPlugin(t, pluginsRoot, "agent/demo/plugin.json", testBuiltinPluginJSON)
	if err := writeBuiltinDefinitions(pluginsRoot, output, testReservedRuntimeNames, nil); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	writeBuiltinPlugin(t, pluginsRoot, "agent/demo/plugin.json", strings.Replace(testBuiltinPluginJSON, "A demo plugin", "Updated demo plugin", 1))
	if err := writeBuiltinDefinitions(pluginsRoot, output, testReservedRuntimeNames, nil); err != nil {
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

func TestGenerateBuiltinDefinitionsRejectsInvalidAuthoring(t *testing.T) {
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
			if _, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil); err == nil {
				t.Fatal("invalid authoring accepted")
			}
		})
	}
}

func TestGenerateBuiltinDefinitionsRejectsReservedCoreBinaryNames(t *testing.T) {
	for _, name := range testReservedRuntimeNames {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			content := strings.Replace(testBuiltinPluginJSON, `"name":"demo","tool":"demo"`, `"name":"`+name+`","tool":"demo"`, 1)
			writeBuiltinPlugin(t, root, "agent/demo/plugin.json", content)
			if _, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil); err == nil || !strings.Contains(err.Error(), "reserved core binary name") {
				t.Fatalf("error=%v, want reserved name rejection", err)
			}
		})
	}
}

func TestGenerateBuiltinDefinitionsIncludesStandardAgentPackage(t *testing.T) {
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
	definitions, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %#v, want one Agent package", definitions)
	}
	payload, err := plugin.DecodeResourcePayload(definitions[0].Spec, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if definitions[0].ID != "demo" || len(payload.Skills) != 1 || payload.Skills[0].Name != "demo" {
		t.Fatalf("Agent package = %#v", definitions[0])
	}
}

func TestGenerateBuiltinDefinitionsSharesOnlyIdenticalCLIRequirements(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*plugin.BinaryResource)
	}{
		{name: "identical"},
		{name: "source", change: func(b *plugin.BinaryResource) { b.Tool = "npm:bun" }},
		{name: "version", change: func(b *plugin.BinaryResource) { b.Version = "2" }},
		{name: "options", change: func(b *plugin.BinaryResource) { b.Options = map[string]any{"bin_path": "other"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"bun", "web"} {
				binary := plugin.BinaryResource{Name: "bun", Tool: "bun", Version: "1", Options: map[string]any{"bin_path": "bin"}}
				if name == "web" && test.change != nil {
					test.change(&binary)
				}
				descriptor, err := json.Marshal(map[string]any{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": name, "extensions": map[string]any{"com.cherryhq.stella": map[string]any{"version": "1", "binaries": []plugin.BinaryResource{binary}}}})
				if err != nil {
					t.Fatal(err)
				}
				writeBuiltinPlugin(t, root, "agent/"+name+"/plugin.json", string(descriptor))
			}
			catalog, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, nil)
			if test.change == nil {
				if err != nil {
					t.Fatal(err)
				}
				if len(catalog) != 2 {
					t.Fatalf("got %d packages, want both owners", len(catalog))
				}
			} else if err == nil || !strings.Contains(err.Error(), "conflicting builtin plugin resource binary") {
				t.Fatalf("got %v, want conflicting binary rejection", err)
			}
		})
	}
}

func TestGenerateBuiltinDefinitionsPreservesComposablePackage(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "agent", "workspace.tools")
	files := map[string]string{
		"plugin.json": `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "workspace.tools",
  "description": "Workspace tools",
  "extensions": {"com.cherryhq.stella": {"version": "1", "binaries": [{"name":"demo-cli","tool":"github:example/demo","version":"1.2.3"}], "oauth": [{"provider":"demo","scopes":["read"],"bindings":[{"credential":"access_token","env_var":"DEMO_ACCESS_TOKEN"}]}]}}
}`,
		"mcp.json":                        `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"docs":{"type":"streamable-http","url":"https://docs.example.test/mcp","headers":{"X-Client-Version":"1"}},"search":{"type":"sse","url":"https://search.example.test/sse"}}}`,
		"skills/workspace-guide/SKILL.md": "---\nname: workspace-guide\ndescription: Use the workspace tools\n---\n\nRead the workspace documentation before making changes.\n",
	}
	for name, content := range files {
		filename := filepath.Join(packageRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	generated, err := generateBuiltinDefinitions(root, testReservedRuntimeNames, map[string]struct{}{"demo": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 1 {
		t.Fatalf("package count = %d, want one package containing all resources", len(generated))
	}
	payload, err := plugin.DecodeResourcePayload(generated[0].Spec, generated[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if generated[0].ID != "workspace.tools" || len(payload.Skills) != 1 || len(payload.Binaries) != 1 || len(payload.MCPServers) != 2 || len(payload.OAuth) != 1 {
		t.Fatalf("package resources were lost: %#v", payload)
	}
	if payload.MCPServers["docs"].Transport != "streamable_http" || payload.MCPServers["search"].Transport != "sse" {
		t.Fatalf("MCP transport conversion was lost: %#v", payload.MCPServers)
	}
}

func TestOAuthConnectionBindingIsExplicitlyUnsupported(t *testing.T) {
	err := plugin.ValidateResourceDeclarations(plugin.ResourcePayload{
		OAuth:      []plugin.OAuthRequirement{{Provider: "demo", Bindings: []plugin.OAuthBinding{{Credential: "access_token", Connection: "docs"}}}},
		MCPServers: map[string]plugin.MCPServerResource{"docs": {URL: "https://example.com/mcp", Transport: "sse", AuthType: "none"}},
	}, "demo", nil)
	if err == nil {
		t.Fatal("unimplemented connection binding was silently accepted")
	}
}

func TestSystemRuntimeGenerationUsesOnlyImmutableReleaseCommands(t *testing.T) {
	catalog := []builtinDefinition{
		makeBuiltinDefinition(t, "fd", plugin.ResourcePayload{Binaries: []plugin.BinaryResource{{Name: "fd", Tool: "github:sharkdp/fd", Version: "10.4.2"}}}),
		makeBuiltinDefinition(t, "injected", plugin.ResourcePayload{Binaries: []plugin.BinaryResource{{Name: "injected", Tool: "npm:injected"}}}),
		makeBuiltinDefinition(t, "xberg", plugin.ResourcePayload{Skills: []plugin.SkillResource{{Name: "xberg"}}}),
	}
	before, err := renderSystemRuntimes(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range binaries.KnownRuntimeNames() {
		if !strings.Contains(string(before), `Name: "`+name+`", Embedded: true`) {
			t.Fatalf("missing embedded release command %s", name)
		}
	}
	for _, name := range []string{"fd", "injected"} {
		if strings.Contains(string(before), `Name: "`+name+`"`) {
			t.Fatalf("authored command %s changed the embedded release map", name)
		}
	}
	if !strings.Contains(string(before), `"builtin:xberg"`) {
		t.Fatal("lost Xberg skill platform restriction")
	}
	for i := range catalog {
		catalog[i].DefaultEnabled = !catalog[i].DefaultEnabled
	}
	after, err := renderSystemRuntimes(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("plugin enablement changed the embedded release map")
	}
}

func makeBuiltinDefinition(t *testing.T, id string, payload plugin.ResourcePayload) builtinDefinition {
	t.Helper()
	spec, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return builtinDefinition{ID: id, DisplayName: id, DefaultEnabled: true, Revision: 1, Spec: spec}
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
