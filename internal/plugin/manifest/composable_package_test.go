package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGenerateBuiltinPluginsPreservesComposablePackage(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "agent", "workspace.tools")
	files := map[string]string{
		"plugin.json": `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "workspace.tools",
  "description": "Workspace tools",
  "extensions": {
    "com.cherryhq.stella": {
      "version": "1",
      "binaries": [{"name":"demo-cli","tool":"github:example/demo","version":"1.2.3"}],
      "oauth": [{
        "provider": "demo",
        "scopes": ["read"],
        "bindings": [
          {"credential":"access_token","env_var":"DEMO_ACCESS_TOKEN"}
        ]
      }]
    }
  }
}`,
		"mcp.json": `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "docs": {"type":"streamable-http","url":"https://docs.example.test/mcp","headers":{"X-Client-Version":"1"}},
    "search": {"type":"sse","url":"https://search.example.test/sse"}
  }
}`,
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

	generated, err := GenerateBuiltinPlugins(root, testReservedRuntimeNames, []string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.Plugins) != 1 {
		t.Fatalf("package count = %d, want one package containing all resources", len(generated.Plugins))
	}
	got := generated.Plugins[0]
	if got.ID != "workspace.tools" || got.Name != "workspace.tools" {
		t.Fatalf("package identity = %q/%q", got.ID, got.Name)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "workspace-guide" {
		t.Fatalf("skill ownership was lost: %+v", got.Skills)
	}
	if len(got.Binaries) != 1 || got.Binaries[0].Name != "demo-cli" || got.Binaries[0].Version != "1.2.3" {
		t.Fatalf("binary declaration was lost: %+v", got.Binaries)
	}
	if len(got.MCPServers) != 2 {
		t.Fatalf("MCP resources = %+v, want both named servers", got.MCPServers)
	}
	docs, search := got.MCPServers["docs"], got.MCPServers["search"]
	if docs.URL != "https://docs.example.test/mcp" || docs.Transport != "streamable_http" || docs.Headers["X-Client-Version"] != "1" {
		t.Fatalf("docs server declaration was lost: %+v", docs)
	}
	if search.URL != "https://search.example.test/sse" || search.Transport != "sse" {
		t.Fatalf("search server declaration was lost: %+v", search)
	}
	wantOAuth := []ManifestOAuthRequirement{{
		Provider: "demo", Scopes: []string{"read"},
		Bindings: []ManifestOAuthBinding{
			{Credential: "access_token", EnvVar: "DEMO_ACCESS_TOKEN"},
		},
	}}
	if !reflect.DeepEqual(got.OAuth, wantOAuth) {
		t.Fatalf("OAuth resource bindings = %+v, want %+v", got.OAuth, wantOAuth)
	}
}

func TestOAuthConnectionBindingIsExplicitlyUnsupported(t *testing.T) {
	errs := validateOAuthBindings(ManifestPluginDefinition{OAuth: []ManifestOAuthRequirement{{Provider: "demo", Bindings: []ManifestOAuthBinding{{Credential: "access_token", Connection: "docs"}}}}, MCPServers: map[string]ManifestMCPServer{"docs": {URL: "https://example.com/mcp"}}}, nil)
	if len(errs) == 0 {
		t.Fatal("unimplemented connection binding was silently accepted")
	}
}
