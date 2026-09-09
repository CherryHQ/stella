package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	internalmcp "github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/toolmeta"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Test the production runner boundary with a real retained runner. The model,
// MCP discovery, and sandbox are fake, but plugin resources and Skill bytes are
// captured through the same filesystem admission path as a live turn.
func TestRunnerFilesystemResourcesRefreshAcrossTurns(t *testing.T) {
	fixture := newRunnerFilesystemFixture(t)
	oldContext := fixture.context(t)
	oldRefs, err := pluginFileSkillRefs(oldContext)
	if err != nil {
		t.Fatal(err)
	}
	oldView, err := skill.CaptureSkillTurnViewFromResources(t.Context(), oldContext.FileResources(), fixture.reads, nil, oldRefs, skill.ViewContext{})
	if err != nil {
		t.Fatal(err)
	}

	provider := &filesystemTurnProvider{}
	session := &filesystemTurnSession{fakeSession: &fakeSession{alive: true}, files: &filesystemFileAccess{root: t.TempDir()}}
	r, err := newRunner(t.Context(), runnerConfig{
		Provider: providerConfig{API: "test", Model: "test-model", APIKey: "test-key", Builder: func(string, string, string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(provider), nil
		}},
		System:              "test",
		PreparedSession:     session,
		PluginContext:       oldContext,
		MCPToolProvider:     fixture.mcp,
		SkillRevisionReader: emptySkillRuntime{},
		SkillReadAuthorizer: fixture.reads,
		ToolMetaRegistry:    toolmeta.NewRegistry(),
		BuiltinParams:       RunnerParams{UserID: "filesystem-user", AgentID: "filesystem-agent"},
		Sandbox: sandbox.Config{
			Paths:  sandbox.Paths{StellaHome: fixture.stellaHome, AgentRoot: fixture.agentRoot, UserRoot: fixture.userRoot},
			UserID: "filesystem-user", AgentID: "filesystem-agent",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if r.session != session {
		t.Fatal("newRunner did not retain the prepared session")
	}

	oldCtx, oldPrepared, err := r.PrepareTurn(t.Context(), oldContext)
	if err != nil {
		t.Fatal(err)
	}
	oldCtx = skill.WithSkillTurnView(oldCtx, oldView)
	oldEnv, ok := sandbox.TurnEnv(oldCtx)
	_, oldToken := oldEnv["OLD_TOKEN"]
	_, newToken := oldEnv["NEW_TOKEN"]
	if !ok || !oldToken || newToken {
		t.Fatalf("old turn env = %#v, want OLD_TOKEN only", oldEnv)
	}
	oldResults := runFilesystemTurn(t, r, oldCtx)
	if !strings.Contains(oldResults.skill, "old skill bytes") || !strings.Contains(oldResults.mcp, "mcp-old") {
		t.Fatalf("old turn results = %#v, provider tools = %#v", oldResults, provider.toolSets)
	}

	fixture.replaceResources(t)
	newContext := fixture.context(t)
	newRefs, err := pluginFileSkillRefs(newContext)
	if err != nil {
		t.Fatal(err)
	}
	newView, err := skill.CaptureSkillTurnViewFromResources(t.Context(), newContext.FileResources(), fixture.reads, nil, newRefs, skill.ViewContext{})
	if err != nil {
		t.Fatal(err)
	}
	newCtx, newPrepared, err := r.PrepareTurn(t.Context(), newContext)
	if err != nil {
		t.Fatal(err)
	}
	newCtx = skill.WithSkillTurnView(newCtx, newView)
	provider.setMCPName("remote_new__ping")
	newEnv, ok := sandbox.TurnEnv(newCtx)
	_, oldToken = newEnv["OLD_TOKEN"]
	_, newToken = newEnv["NEW_TOKEN"]
	if !ok || oldToken || !newToken {
		t.Fatalf("new turn env = %#v, want NEW_TOKEN only", newEnv)
	}
	if oldPrepared.FileResources()[0].Key == newPrepared.FileResources()[0].Key && oldPrepared.FileResources()[0].Digest == newPrepared.FileResources()[0].Digest {
		t.Fatal("resource capture did not change after filesystem mutation")
	}
	if r.session != session {
		t.Fatal("PrepareTurn replaced the retained sandbox session")
	}
	fixture.assertLatestMCP(t, "remote-new")
	provider.resetTurn()
	newResults := runFilesystemTurn(t, r, newCtx)
	if !strings.Contains(newResults.skill, "new skill bytes") || !strings.Contains(newResults.mcp, "mcp-new") {
		t.Fatalf("new turn results = %#v", newResults)
	}
	if strings.Contains(newResults.mcp, "tool not found") || !strings.Contains(newResults.mcp, "mcp-new") {
		t.Fatalf("new MCP projection = %q", newResults.mcp)
	}

	// The old captured bytes stay stable, but its per-call read decision is
	// still live. Revoking the old package hides it instead of reopening bytes.
	fixture.reads.deny(fixture.packageID(oldContext))
	provider.resetTurn()
	oldCtx, _, err = r.PrepareTurn(t.Context(), oldContext)
	if err != nil {
		t.Fatal(err)
	}
	oldCtx = skill.WithSkillTurnView(oldCtx, oldView)
	provider.setMCPName("remote__ping")
	revoked := runFilesystemTurn(t, r, oldCtx)
	if strings.Contains(revoked.skill, "old skill bytes") {
		t.Fatalf("revoked old skill leaked through captured turn: %q", revoked.skill)
	}
}

type filesystemTurnResults struct{ skill, mcp string }

func runFilesystemTurn(t *testing.T, r *runner, ctx context.Context) filesystemTurnResults {
	t.Helper()
	var result filesystemTurnResults
	for event := range r.Chat(ctx, nil, "inspect resources") {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.ToolUse == nil || (event.ToolUse.Status != "done" && event.ToolUse.Status != "error") {
			continue
		}
		switch event.ToolUse.Tool {
		case "skill_load":
			result.skill += event.ToolUse.Content
		case "remote__ping":
			result.mcp += event.ToolUse.Content
		case "code":
			if strings.Contains(event.ToolUse.Content, "skill bytes") {
				result.skill += event.ToolUse.Content
			}
			if strings.Contains(event.ToolUse.Content, "mcp-") {
				result.mcp += event.ToolUse.Content
			}
		}
	}
	return result
}

type filesystemTurnProvider struct {
	mu       sync.Mutex
	calls    int
	toolSets [][]string
	mcpName  string
}

func (p *filesystemTurnProvider) API() string { return "test" }

func (p *filesystemTurnProvider) resetTurn() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = 0
	p.toolSets = nil
}

func (p *filesystemTurnProvider) setMCPName(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mcpName = name
}

func (p *filesystemTurnProvider) Stream(_ context.Context, _ ai.Model, ctx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	names := make([]string, 0, len(ctx.Tools))
	for _, definition := range ctx.Tools {
		names = append(names, definition.Name)
	}
	p.toolSets = append(p.toolSets, names)
	p.mu.Unlock()

	out := providers.NewChannelEventStream(2)
	go func() {
		switch call % 3 {
		case 0:
			out.Emit(ai.EventToolCallDelta{ID: "skill-call", Name: "code", Arguments: `{"code":"return await tools.invoke('skill_load', {name: 'docs'});"}`})
			out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
		case 1:
			p.mu.Lock()
			mcpName := p.mcpName
			p.mu.Unlock()
			if mcpName == "" {
				mcpName = "remote__ping"
			}
			out.Emit(ai.EventToolCallDelta{ID: "mcp-call", Name: "code", Arguments: fmt.Sprintf(`{"code":"return await tools.invoke('%s', {});"}`, mcpName)})
			out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
		default:
			out.Emit(ai.EventTextDelta{Text: "done"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
		}
		out.Finish(nil)
	}()
	return out, nil
}

type filesystemMCP struct {
	mu    sync.Mutex
	calls [][]plugin.FileResource
}

func (m *filesystemMCP) NewFileSession() *internalmcp.FileSession {
	return internalmcp.NewFileSession(nil)
}

func (m *filesystemMCP) ToolsForFileSession(_ context.Context, _ *internalmcp.FileSession, resources []plugin.FileResource, _ authz.Authority) (pkgplugins.MCPToolSnapshot, error) {
	m.mu.Lock()
	m.calls = append(m.calls, resources)
	m.mu.Unlock()
	for _, resource := range resources {
		if resource.Key.Kind != plugin.ResourceMCP || resource.Disabled || resource.Forbidden {
			continue
		}
		name := "remote__ping"
		if resource.Key.Name == "remote-new" {
			name = "remote_new__ping"
		}
		return pkgplugins.MCPToolSnapshot{
			Tools:     []tools.Tool{filesystemMCPTool{name: name, resourceID: resource.Key.ID(), server: resource.Key.Name, result: map[string]string{"remote": "mcp-old", "remote-new": "mcp-new"}[resource.Key.Name]}},
			Directory: []pkgplugins.MCPDirectoryEntry{{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: resource.Key.ID()}, ServerKey: resource.Key.Name, Ready: true}},
		}, nil
	}
	return pkgplugins.MCPToolSnapshot{}, nil
}

type filesystemMCPTool struct {
	name, resourceID, server, result string
}

func (t filesystemMCPTool) Definition() tools.Definition {
	return tools.Definition{Name: t.name, Description: "test MCP"}
}

func (t filesystemMCPTool) Execute(context.Context, map[string]any) (string, error) {
	return t.result, nil
}

func (t filesystemMCPTool) PluginToolIdentity() (string, string, string, bool) {
	return t.resourceID, t.server, "ping", true
}

type filesystemTurnSession struct {
	*fakeSession
	files pkgsandbox.FileAccess
}

func (s *filesystemTurnSession) Files() pkgsandbox.FileAccess { return s.files }

type filesystemFileAccess struct{ root string }

func (a *filesystemFileAccess) path(name string) string {
	return filepath.Join(a.root, filepath.FromSlash(strings.TrimPrefix(name, "/")))
}

func (a *filesystemFileAccess) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(a.path(name))
}

func (a *filesystemFileAccess) ReadDir(name string) ([]pkgsandbox.DirEntry, error) {
	entries, err := os.ReadDir(a.path(name))
	if err != nil {
		return nil, err
	}
	out := make([]pkgsandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, _ := entry.Info()
		out = append(out, pkgsandbox.DirEntry{Name: entry.Name(), IsDir: entry.IsDir(), Size: info.Size()})
	}
	return out, nil
}

func (a *filesystemFileAccess) Stat(name string) (pkgsandbox.FileInfo, error) {
	info, err := os.Stat(a.path(name))
	if err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	return pkgsandbox.FileInfo{IsDir: info.IsDir(), Size: info.Size()}, nil
}

func (a *filesystemFileAccess) WriteFile(name string, content []byte, mode fs.FileMode) error {
	return os.WriteFile(a.path(name), content, mode)
}

func (a *filesystemFileAccess) ProjectFiles(name string, files []pkgsandbox.ProjectedFile) error {
	return a.project(name, files)
}

func (a *filesystemFileAccess) ProjectTempFiles(name string, files []pkgsandbox.ProjectedFile) (string, error) {
	if err := a.project(name, files); err != nil {
		return "", err
	}
	return a.path(name), nil
}

func (a *filesystemFileAccess) project(name string, files []pkgsandbox.ProjectedFile) error {
	root := a.path(name)
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.Content, file.Mode); err != nil {
			return err
		}
	}
	return nil
}

type filesystemReadAuthorizer struct {
	mu     sync.Mutex
	denied map[string]bool
}

func (a *filesystemReadAuthorizer) BeginRead(context.Context) (skill.SkillReadDecision, error) {
	return a, nil
}

func (a *filesystemReadAuthorizer) AllowRead(_ context.Context, id, _, _, _ string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.denied[id], nil
}

func (a *filesystemReadAuthorizer) deny(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.denied[id] = true
}

type runnerFilesystemFixture struct {
	base, stellaHome, userRoot, agentRoot string
	manager                               home.Workspace
	reads                                 *filesystemReadAuthorizer
	mcp                                   *filesystemMCP
}

func newRunnerFilesystemFixture(t *testing.T) *runnerFilesystemFixture {
	t.Helper()
	base := t.TempDir()
	stellaHome := t.TempDir()
	userRoot := filepath.Join(stellaHome, "users", "filesystem-user")
	agentRoot := filepath.Join(userRoot, "agents", "filesystem-agent")
	for _, dir := range []string{agentRoot, filepath.Join(userRoot, "data")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pluginDir := filepath.Join(base, ".agents", "plugins", "demo", "skills", "docs")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFilesystemFixture(t, base, "old skill bytes", "OLD_TOKEN")
	db := dbtest.New(t)
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	reads := &filesystemReadAuthorizer{denied: make(map[string]bool)}
	return &runnerFilesystemFixture{base: base, stellaHome: stellaHome, userRoot: userRoot, agentRoot: agentRoot, manager: manager, reads: reads, mcp: &filesystemMCP{}}
}

func writeFilesystemFixture(t *testing.T, base, skillText, envName string) {
	t.Helper()
	pluginRoot := filepath.Join(base, ".agents", "plugins", "demo")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"$schema":%q,"name":"demo","version":"1.0.0","extensions":{"com.cherryhq.stella":{"version":"1","session_env":[{"env_var":%q,"source":"static","required":true}]}}}`, agentpackage.PluginSchemaV1, envName)
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "skills", "docs", "SKILL.md"), []byte("---\nname: docs\ndescription: docs\n---\n"+skillText+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpRoot := filepath.Join(base, ".agents", "mcp")
	if err := os.MkdirAll(mcpRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mcp := `{"url":"https://example.invalid/mcp","transport":"streamable_http"}`
	if err := os.WriteFile(filepath.Join(mcpRoot, "remote.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *runnerFilesystemFixture) context(t *testing.T) PluginContext {
	t.Helper()
	resources, err := plugin.DiscoverResources(t.Context(), []plugin.ResourceRoot{{Scope: plugin.ScopeSystem, Open: func(ctx context.Context) (home.RootOperations, error) {
		return f.manager.OpenRoot(ctx, home.WorkspaceRequest{}, home.RootSystemResources, home.RootReadOnly)
	}}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewSystemAuthority("runner-filesystem-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agentruntime.NewFilePluginContext(authority, resources)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func (f *runnerFilesystemFixture) replaceResources(t *testing.T) {
	t.Helper()
	writeFilesystemFixture(t, f.base, "new skill bytes", "NEW_TOKEN")
	if err := os.Remove(filepath.Join(f.base, ".agents", "mcp", "remote.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.base, ".agents", "mcp", "remote-new.json"), []byte(`{"url":"https://example.invalid/mcp","transport":"streamable_http"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *runnerFilesystemFixture) packageID(ctx PluginContext) string {
	for _, resource := range ctx.FileResources() {
		if resource.Key.Kind == plugin.ResourcePlugin {
			return resource.Key.ID()
		}
	}
	return ""
}

func (f *runnerFilesystemFixture) assertLatestMCP(t *testing.T, want string) {
	t.Helper()
	f.mcp.mu.Lock()
	defer f.mcp.mu.Unlock()
	if len(f.mcp.calls) == 0 {
		t.Fatal("MCP provider was not called")
	}
	for _, resource := range f.mcp.calls[len(f.mcp.calls)-1] {
		if resource.Key.Kind == plugin.ResourceMCP && resource.Key.Name == want {
			return
		}
	}
	t.Fatalf("latest MCP resources = %#v, want %q", f.mcp.calls[len(f.mcp.calls)-1], want)
}
