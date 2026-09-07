package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndWriteAssetsIsDeterministic(t *testing.T) {
	pluginsRoot := t.TempDir()
	owner := filepath.Join(pluginsRoot, "tools", "demo")
	if err := os.MkdirAll(filepath.Join(owner, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "assets.yaml"), []byte("assets:\n  - name: demo\n    source: skills/demo\n    logical_root: plugins/tool/demo/demo\n    owner_plugin_id: tool/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "skills", "demo", "SKILL.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	assets, err := discover(pluginsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Name != "demo" || len(assets[0].Files) != 1 {
		t.Fatalf("discovered assets = %#v", assets)
	}
	output := filepath.Join(pluginsRoot, "builtin_assets_gen.go")
	if err := writeGenerated(output, assets); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(want), `//go:embed "tools/demo/skills/demo/SKILL.md"`) {
		t.Fatalf("generated output omits explicit file embed: %s", want)
	}
	if err := writeGenerated(output, assets); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("repeated generation changed output")
	}
}

func TestDiscoverRejectsUnknownOwnerShape(t *testing.T) {
	pluginsRoot := t.TempDir()
	dir := filepath.Join(pluginsRoot, "guidance")
	if err := os.MkdirAll(filepath.Join(dir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets.yaml"), []byte("assets:\n  - name: demo\n    source: skills/demo\n    logical_root: core/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "demo", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(pluginsRoot); err == nil || !strings.Contains(err.Error(), "no owner") {
		t.Fatalf("discover error = %v, want missing owner", err)
	}
}

func TestDiscoverReadsStandardAgentPackageSkills(t *testing.T) {
	pluginsRoot := t.TempDir()
	root := filepath.Join(pluginsRoot, "agent", "demo")
	if err := os.MkdirAll(filepath.Join(root, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "1.0.0",
  "description": "Demo Agent package"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: Demo skill\n---\n\nUse the demo skill.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "demo", "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", "references", "plugin.json"), []byte(`{"name":"attachment"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	assets, err := discover(pluginsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 {
		t.Fatalf("discovered assets = %#v", assets)
	}
	got := assets[0]
	if got.Name != "demo" || got.SourceRoot != "agent/demo/skills/demo" || got.LogicalRoot != "plugins/agent/demo/demo" || got.OwnerPluginID != "demo" || len(got.Files) != 2 || got.Files[0] != "SKILL.md" || got.Files[1] != "references/plugin.json" {
		t.Fatalf("discovered Agent asset = %#v", got)
	}
}

func TestDiscoverRejectsAgentPackageRuntimeAndLegacyComponents(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "legacy assets", file: "assets.yaml", want: "cannot also contain assets.yaml"},
		{name: "legacy plugin", file: "plugin.yaml", want: "cannot also contain plugin.yaml"},
		{name: "MCP", file: "mcp.json", want: "declares MCP servers"},
		{name: "Stella extension", want: "declares Stella runtime requirements"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pluginsRoot := t.TempDir()
			root := filepath.Join(pluginsRoot, "agent", "demo")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`
			switch test.name {
			case "MCP":
				manifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`
				if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"remote":{"type":"sse","url":"https://mcp.example.test/sse"}}}`), 0o644); err != nil {
					t.Fatal(err)
				}
			case "Stella extension":
				manifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","extensions":{"com.cherryhq.stella":{"version":"1","binaries":[{"name":"bun","tool":"mise","version":"1.2.3"}]}}}`
			}
			if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			if test.file != "" && test.name != "MCP" {
				if err := os.WriteFile(filepath.Join(root, test.file), []byte("assets: []\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := discover(pluginsRoot); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("discover error = %v, want %q", err, test.want)
			}
		})
	}
}
