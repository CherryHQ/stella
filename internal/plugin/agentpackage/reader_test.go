package agentpackage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPlainPackagePreservesSkillAndAllowsPluginPeriods(t *testing.T) {
	root := copyFixture(t, "plain")
	pkg, diagnostics := Load(root)
	if pkg == nil || diagnostics.HasErrors() {
		t.Fatalf("Load() package=%#v diagnostics=%+v", pkg, diagnostics)
	}
	if pkg.Manifest.Name != "acme.tools" {
		t.Fatalf("manifest name = %q, want acme.tools", pkg.Manifest.Name)
	}
	if len(pkg.Skills) != 1 || pkg.Skills[0].Name != "acme-tools" {
		t.Fatalf("skills = %#v, want one acme-tools skill", pkg.Skills)
	}
	content, err := os.ReadFile(filepath.Join(root, "skills", "acme-tools", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pkg.Skills[0].Content) != string(content) || pkg.Skills[0].Mode.Perm() != 0o644 {
		t.Fatalf("skill bytes/mode were not preserved: mode=%#o", pkg.Skills[0].Mode.Perm())
	}
}

func TestLoadDiagnosticsUnknownManifestAndOpaqueExtension(t *testing.T) {
	root := newPackage(t, `{
  "$schema": "`+PluginSchemaV1+`",
  "name": "opaque-extension",
  "unknown": {"secret": "do not inspect"},
  "extensions": {"com.example.unknown": {"huge": 1e1000}}
}`)
	writeSkill(t, root, "demo", "demo skill")
	pkg, diagnostics := Load(root)
	if pkg == nil || len(pkg.Skills) != 1 {
		t.Fatalf("package/skills = %#v, diagnostics=%+v", pkg, diagnostics)
	}
	if !hasCode(diagnostics, "manifest.unknown_field") || hasCode(diagnostics, "extension") {
		t.Fatalf("diagnostics = %+v, want only manifest unknown-field explanation", diagnostics)
	}
	if strict := ValidateAuthoring(root); !hasCode(strict, "manifest.unknown_field") || !strict.HasErrors() {
		t.Fatalf("strict diagnostics = %+v, want unknown manifest field error", strict)
	}
}

func TestOptionalManifestFieldsAndStringTypes(t *testing.T) {
	root := newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"metadata","author":{}}`)
	if pkg, diagnostics := Load(root); pkg == nil || diagnostics.HasErrors() {
		t.Fatalf("empty author should be valid: package=%#v diagnostics=%+v", pkg, diagnostics)
	}
	root = newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"metadata","author":{"name":"V"}}`)
	if pkg, diagnostics := Load(root); pkg == nil || diagnostics.HasErrors() {
		t.Fatalf("partial author should be valid: package=%#v diagnostics=%+v", pkg, diagnostics)
	}
	for _, manifest := range []string{
		`{"$schema":"` + PluginSchemaV1 + `","name":"metadata","version":null}`,
		`{"$schema":"` + PluginSchemaV1 + `","name":"metadata","keywords":["ok",null]}`,
		`{"$schema":"` + PluginSchemaV1 + `","name":"metadata","author":{"name":null}}`,
	} {
		if pkg, diagnostics := Load(newPackage(t, manifest)); pkg != nil || !diagnostics.HasErrors() {
			t.Fatalf("manifest=%s package=%#v diagnostics=%+v, want fatal string type error", manifest, pkg, diagnostics)
		}
	}
}

func TestSkillStringTypesAndUnicodeLowercaseNames(t *testing.T) {
	root := newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"skill-types"}`)
	writeSkill(t, root, "bad", "bad skill")
	if err := os.WriteFile(filepath.Join(root, "skills", "bad", "SKILL.md"), []byte("---\nname: bad\ndescription: 123\nmetadata:\n  n: 1\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, root, "café", "unicode skill")
	pkg, diagnostics := Load(root)
	if pkg == nil || len(pkg.Skills) != 1 || pkg.Skills[0].Name != "café" || !hasCode(diagnostics, "skill.invalid") {
		t.Fatalf("package=%#v diagnostics=%+v, want only unicode skill", pkg, diagnostics)
	}
}

func TestStrictSchemaExtensionAndDiscoveryBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		wantCode string
	}{
		{name: "missing schema", manifest: `{"name":"missing-schema"}`, wantCode: "manifest.schema"},
		{name: "empty schema", manifest: `{"$schema":"","name":"empty-schema"}`, wantCode: "manifest.schema"},
		{name: "unsupported schema", manifest: `{"$schema":"https://agent-plugins.org/schemas/9.9.9/plugin.schema.json","name":"unsupported-schema"}`, wantCode: "manifest.schema"},
		{name: "extension version", manifest: `{"$schema":"` + PluginSchemaV1 + `","name":"extension-version","extensions":{"` + StellaNamespace + `":{"version":"2"}}}`, wantCode: "extension.version"},
		{name: "native implementation field", manifest: `{"$schema":"` + PluginSchemaV1 + `","name":"native-field","extensions":{"` + StellaNamespace + `":{"version":"1","native_implementation":"email"}}}`, wantCode: "extension.field"},
		{name: "unknown binary field", manifest: `{"$schema":"` + PluginSchemaV1 + `","name":"nested-field","extensions":{"` + StellaNamespace + `":{"version":"1","binaries":[{"name":"bun","tool":"mise","unknown":true}]}}}`, wantCode: "extension.binaries"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if diagnostics := ValidateAuthoring(newPackage(t, test.manifest)); !diagnostics.HasErrors() || !hasCode(diagnostics, test.wantCode) {
				t.Fatalf("diagnostics=%+v, want %s error", diagnostics, test.wantCode)
			}
		})
	}

	root := newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"discovery"}`)
	writeSkill(t, root, "good", "good skill")
	writeSkill(t, root, "bad", "bad skill")
	if err := os.WriteFile(filepath.Join(root, "skills", "bad", "SKILL.md"), []byte("---\nname: bad\ndescription: 123\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "nested", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "nested", "deeper", "SKILL.md"), []byte("---\nname: deeper\ndescription: too deep\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "lowercase"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "lowercase", "skill.md"), []byte("---\nname: lowercase\ndescription: wrong case\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	crlf := []byte("---\r\nname: crlf\r\ndescription: CRLF skill\r\n---\r\n\r\nbody\r\n")
	writeSkill(t, root, "crlf", "placeholder")
	if err := os.WriteFile(filepath.Join(root, "skills", "crlf", "SKILL.md"), crlf, 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, diagnostics := Load(root)
	if pkg == nil || len(pkg.Skills) != 2 || !hasCode(diagnostics, "skill.invalid") {
		t.Fatalf("package=%#v diagnostics=%+v, want good/crlf only", pkg, diagnostics)
	}
	for _, skill := range pkg.Skills {
		if skill.Name == "crlf" && string(skill.Content) != string(crlf) {
			t.Fatal("CRLF skill bytes were not preserved")
		}
	}
}

func TestLoadMultipleMCPEntriesKeepsIndependentValidServers(t *testing.T) {
	root := newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"multi-mcp"}`)
	writeSkill(t, root, "usable", "usable skill")
	writeJSON(t, filepath.Join(root, "mcp.json"), map[string]any{
		"$schema": MCPV1Schema,
		"mcpServers": map[string]any{
			"remote":      map[string]any{"type": "streamable-http", "url": "https://mcp.example.test/mcp"},
			"legacy":      map[string]any{"type": "sse", "url": "https://mcp.example.test/sse"},
			"local":       map[string]any{"type": "stdio", "command": "./bin/server"},
			"broken":      map[string]any{"type": "telepathy", "url": "https://ignored.example"},
			"null-header": map[string]any{"type": "sse", "url": "https://mcp.example.test/sse", "headers": map[string]any{"X-Null": nil}},
		},
	})
	pkg, diagnostics := Load(root)
	if pkg == nil || len(pkg.Skills) != 1 || len(pkg.MCPServers) != 2 {
		t.Fatalf("package=%#v diagnostics=%+v", pkg, diagnostics)
	}
	if pkg.MCPServers[0].Name != "legacy" || pkg.MCPServers[1].Name != "remote" {
		t.Fatalf("MCP servers = %#v, want sorted legacy/remote", pkg.MCPServers)
	}
	if !hasCode(diagnostics, "mcp.server.unsupported_transport") || !hasCode(diagnostics, "mcp.server.transport") {
		t.Fatalf("diagnostics = %+v, want stdio and unknown transport diagnostics", diagnostics)
	}
	if !hasCode(diagnostics, "mcp.server.headers") {
		t.Fatalf("diagnostics = %+v, want null header diagnostic", diagnostics)
	}
	if strict := ValidateAuthoring(root); !strict.HasErrors() || !hasCode(strict, "mcp.server.unsupported_transport") {
		t.Fatalf("strict diagnostics = %+v, want unsupported stdio authoring error", strict)
	}
}

func TestMalformedManifestHasNoComponents(t *testing.T) {
	root := newPackage(t, `{"$schema":`)
	writeSkill(t, root, "hidden", "hidden skill")
	writeJSON(t, filepath.Join(root, "mcp.json"), map[string]any{"$schema": MCPV1Schema, "mcpServers": map[string]any{}})
	pkg, diagnostics := Load(root)
	if pkg != nil || len(diagnostics) == 0 || !hasCode(diagnostics, "manifest.invalid") {
		t.Fatalf("package=%#v diagnostics=%+v, want fatal malformed manifest", pkg, diagnostics)
	}
}

func TestPackagePathContainmentAllowsInternalAndRejectsEscapes(t *testing.T) {
	root := newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"paths"}`)
	if err := os.MkdirAll(filepath.Join(root, "skills", "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "escape"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared.md"), []byte("---\nname: inside\ndescription: internal\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("---\nname: escape\ndescription: outside\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "shared.md"), filepath.Join(root, "skills", "inside", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills", "escape", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	pkg, diagnostics := Load(root)
	if pkg == nil || len(pkg.Skills) != 1 || pkg.Skills[0].Name != "inside" {
		t.Fatalf("package=%#v diagnostics=%+v, want only internal skill", pkg, diagnostics)
	}
	if !hasCode(diagnostics, "path.escape") {
		t.Fatalf("diagnostics = %+v, want path escape", diagnostics)
	}
}

func TestManifestSymlinkEscapeIsFatalAndMCPSymlinkEscapeIsComponentLocal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "plugin.json"), []byte(`{"$schema":"`+PluginSchemaV1+`","name":"escape"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "plugin.json"), filepath.Join(root, "plugin.json")); err != nil {
		t.Fatal(err)
	}
	if pkg, diagnostics := Load(root); pkg != nil || !hasCode(diagnostics, "path.escape") {
		t.Fatalf("manifest escape package=%#v diagnostics=%+v", pkg, diagnostics)
	}

	root = newPackage(t, `{"$schema":"`+PluginSchemaV1+`","name":"mcp-escape"}`)
	writeSkill(t, root, "kept", "kept skill")
	if err := os.WriteFile(filepath.Join(outside, "mcp.json"), []byte(`{"$schema":"`+MCPV1Schema+`","mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "mcp.json"), filepath.Join(root, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	pkg, diagnostics := Load(root)
	if pkg == nil || len(pkg.Skills) != 1 || len(pkg.MCPServers) != 0 || !hasCode(diagnostics, "path.escape") {
		t.Fatalf("MCP escape package=%#v diagnostics=%+v", pkg, diagnostics)
	}
}

func TestStellaExtensionIsDeclarationsOnly(t *testing.T) {
	root := newPackage(t, `{
  "$schema": "`+PluginSchemaV1+`",
  "name": "stella-declarations",
  "extensions": {
    "com.cherryhq.stella": {
      "version": "1",
      "binaries": [{"name":"bun","tool":"mise","version":"1.2.3","options":{"channel":"stable"}}],
      "session_env": [{"env_var":"GH_TOKEN","source":"oauth.github","required":true}],
      "oauth": [{"provider":"github","scopes":["repo"],"bindings":[{"credential":"access_token","env_var":"GH_TOKEN"}]}]
    }
  }
}`)
	pkg, diagnostics := Load(root)
	if pkg == nil || pkg.Extension == nil || diagnostics.HasErrors() {
		t.Fatalf("package=%#v diagnostics=%+v", pkg, diagnostics)
	}
	if len(pkg.Extension.Binaries) != 1 || len(pkg.Extension.OAuth) != 1 {
		t.Fatalf("Stella extension = %#v", pkg.Extension)
	}
	if got := strings.Join([]string{pkg.Extension.Binaries[0].Name, pkg.Extension.SessionEnv[0].EnvVar}, ":"); got != "bun:GH_TOKEN" {
		t.Fatalf("declarations = %s", got)
	}
}

func newPackage(t *testing.T, manifest string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSkill(t *testing.T, root, name, description string) {
	t.Helper()
	directory := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", name))); err != nil {
		t.Fatal(err)
	}
	return root
}

func hasCode(diagnostics Diagnostics, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code || strings.HasPrefix(diagnostic.Code, code+".") {
			return true
		}
	}
	return false
}
