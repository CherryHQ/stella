package plugin

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func filesystemTestRoots(t *testing.T) ([]ResourceRoot, []string) {
	t.Helper()
	db := dbtest.New(t)
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES('00000000-0000-4000-8000-000000000123','files@example.invalid'); INSERT INTO agent(id,name,workspace) VALUES('file-agent','Files','')`); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	roots := []ResourceRoot{{Scope: ScopeSystem}, {Scope: ScopeSystemAgent, AgentID: "file-agent"}, {Scope: ScopeUser, UserID: "00000000-0000-4000-8000-000000000123"}, {Scope: ScopeUserAgent, UserID: "00000000-0000-4000-8000-000000000123", AgentID: "file-agent"}}
	scopes := []home.RootScope{home.RootSystemResources, home.RootSystemAgentResources, home.RootUserResources, home.RootUserAgentResources}
	paths := []string{filepath.Join(base, ".agents"), filepath.Join(base, "agents/file-agent/.agents"), filepath.Join(base, "users/00000000-0000-4000-8000-000000000123/.agents"), filepath.Join(base, "users/00000000-0000-4000-8000-000000000123/agents/file-agent/.agents")}
	for i := range roots {
		if err := os.MkdirAll(paths[i], 0o755); err != nil {
			t.Fatal(err)
		}
		req := home.WorkspaceRequest{UserID: roots[i].UserID, AgentID: roots[i].AgentID}
		roots[i].Open = func(ctx context.Context) (home.RootOperations, error) {
			return manager.OpenRoot(ctx, req, scopes[i], home.RootReadOnly)
		}
	}

	return roots, paths
}

func writeFilesystemTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	target := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemResourceSelection(t *testing.T) {
	roots, paths := filesystemTestRoots(t)
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"example"}`
	for _, root := range paths {
		writeFilesystemTestFile(t, root, "plugins/example/plugin.json", manifest)
	}
	capture := func() FileResource {
		t.Helper()
		resources, err := DiscoverResources(t.Context(), roots)
		if err != nil {
			t.Fatal(err)
		}
		if len(resources) != 1 {
			t.Fatalf("resources: %+v", resources)
		}
		return resources[0]
	}
	for i := len(paths) - 1; i >= 0; i-- {
		resource := capture()
		if resource.Key.Scope != roots[i].Scope || resource.Package == nil {
			t.Fatalf("winner: %+v", resource)
		}
		if i > 0 {
			if err := os.RemoveAll(filepath.Join(paths[i], "plugins")); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFilesystemTestFile(t, paths[3], "settings.json", `{"disabled":["plugin:example"]}`)
	if resource := capture(); !resource.Disabled || resource.Key.Scope != ScopeUserAgent {
		t.Fatalf("negative winner: %+v", resource)
	}
	if err := os.Remove(filepath.Join(paths[3], "settings.json")); err != nil {
		t.Fatal(err)
	}
	writeFilesystemTestFile(t, paths[3], "plugins/example/plugin.json", `broken`)
	if resource := capture(); resource.Package != nil || resource.Key.Scope != ScopeUserAgent || len(resource.Diagnostics) == 0 {
		t.Fatalf("bad winner fell back: %+v", resource)
	}
	writeFilesystemTestFile(t, paths[3], "plugins/example/plugin.json", manifest)
	writeFilesystemTestFile(t, paths[0], "settings.json", `{"forbidden":["plugin:example"]}`)
	if !capture().Disabled {
		t.Fatal("user copy bypassed administrator prohibition")
	}
	writeFilesystemTestFile(t, paths[3], "settings.json", `{"forbidden":["plugin:example"]}`)
	if _, err := DiscoverResources(t.Context(), roots); err == nil {
		t.Fatal("personal administrator policy accepted")
	}
}

func TestFilesystemResourceCapture(t *testing.T) {
	roots, paths := filesystemTestRoots(t)
	root := paths[2]
	writeFilesystemTestFile(t, root, "skills/example/SKILL.md", "first")
	writeFilesystemTestFile(t, root, "skills/example/scripts/run.sh", "echo first")
	before, err := DiscoverResources(t.Context(), roots)
	if err != nil || len(before) != 1 {
		t.Fatalf("capture: %v %v", before, err)
	}
	writeFilesystemTestFile(t, root, "skills/example/SKILL.md", "second")
	writeFilesystemTestFile(t, root, "skills/example/scripts/run.sh", "echo second")
	after, err := DiscoverResources(t.Context(), roots)
	if err != nil || len(after) != 1 {
		t.Fatalf("capture: %v %v", after, err)
	}
	if before[0].Content.Digest == after[0].Content.Digest {
		t.Fatal("edit did not change capture")
	}
	for name, want := range map[string]string{"SKILL.md": "first", "scripts/run.sh": "echo first"} {
		data, err := fs.ReadFile(before[0].Content.FS(), name)
		if err != nil || string(data) != want {
			t.Fatalf("captured %s changed: %q %v", name, data, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(root, "skills/example")); err != nil {
		t.Fatal(err)
	}
	resources, err := DiscoverResources(t.Context(), roots)
	if err != nil || len(resources) != 0 {
		t.Fatalf("deleted resource remains: %v %v", resources, err)
	}
	writeFilesystemTestFile(t, root, "skills/example/SKILL.md", "safe")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "skills/example/escape")); err != nil {
		t.Fatal(err)
	}
	resources, err = DiscoverResources(t.Context(), roots)
	if err != nil || len(resources) != 1 || resources[0].Content != nil || len(resources[0].Diagnostics) == 0 {
		t.Fatalf("symlink capture accepted: %v %v", resources, err)
	}
}

func TestFilesystemResourceConflicts(t *testing.T) {
	roots, paths := filesystemTestRoots(t)
	skill := "---\nname: shared\ndescription: Shared tool\n---\nInstructions"
	for _, name := range []string{"one", "two"} {
		writeFilesystemTestFile(t, paths[0], "plugins/"+name+"/plugin.json", `{"$schema":"`+agentpackage.PluginSchemaV1+`","name":"`+name+`"}`)
		writeFilesystemTestFile(t, paths[0], "plugins/"+name+"/skills/shared/SKILL.md", skill)
	}
	resources, err := DiscoverResources(t.Context(), roots)
	if err != nil || len(resources) != 2 {
		t.Fatalf("capture: %v %v", resources, err)
	}
	for _, resource := range resources {
		if len(resource.Skills) != 0 || len(resource.Diagnostics) == 0 {
			t.Fatalf("ambiguous Skill selected: %+v", resource)
		}
	}
	writeFilesystemTestFile(t, paths[2], "skills/shared/SKILL.md", skill)
	resources, err = DiscoverResources(t.Context(), roots)
	if err != nil || len(resources) != 3 {
		t.Fatalf("capture: %v %v", resources, err)
	}
	for _, resource := range resources {
		if resource.Key.Kind == ResourceSkill && (resource.Content == nil || len(resource.Diagnostics) != 0) {
			t.Fatal("independent Skill did not win")
		}
		if resource.Key.Kind == ResourcePlugin && (len(resource.Skills) != 0 || len(resource.Diagnostics) != 0) {
			t.Fatalf("explicit override still conflicts: %+v", resource)
		}
	}
	if err := os.RemoveAll(filepath.Join(paths[2], "skills")); err != nil {
		t.Fatal(err)
	}
	writeFilesystemTestFile(t, paths[0], "settings.json", `{"forbidden":["skill:shared"]}`)
	resources, err = DiscoverResources(t.Context(), roots)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		if resource.Key.Kind == ResourcePlugin && len(resource.Skills) != 0 {
			t.Fatal("package bypassed forbidden Skill name")
		}
	}
}

func TestFilesystemResourceMCPDeclarations(t *testing.T) {
	roots, paths := filesystemTestRoots(t)
	writeFilesystemTestFile(t, paths[2], "mcp/remote.json", `{"url":"https://example.invalid/mcp","transport":"streamable_http","auth_type":"oauth"}`)
	writeFilesystemTestFile(t, paths[0], "plugins/example/plugin.json", `{"$schema":"`+agentpackage.PluginSchemaV1+`","name":"example","extensions":{"com.cherryhq.stella":{"version":"1","mcp_auth":{"remote":{"auth_type":"oauth"}}}}}`)
	writeFilesystemTestFile(t, paths[0], "plugins/example/mcp.json", `{"$schema":"`+agentpackage.MCPV1Schema+`","mcpServers":{"remote":{"type":"streamable-http","url":"https://example.invalid/mcp"}}}`)
	resources, err := DiscoverResources(t.Context(), roots)
	if err != nil || len(resources) != 2 {
		t.Fatalf("capture: %v %v", resources, err)
	}
	for _, resource := range resources {
		d, ok := resource.MCP["remote"]
		if !ok || d.Type != "oauth" || d.Mode != "per_user" || d.Transport != "streamable_http" {
			t.Fatalf("different declaration contract: %+v", resource)
		}
	}
	writeFilesystemTestFile(t, paths[2], "mcp/remote.json", `{"url":"https://example.invalid/mcp","transport":"streamable_http","token":"secret"}`)
	resources, err = DiscoverResources(t.Context(), roots)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		if resource.Key.Kind == ResourceMCP && (len(resource.MCP) != 0 || len(resource.Diagnostics) == 0) {
			t.Fatal("secret-bearing declaration accepted")
		}
	}
}
