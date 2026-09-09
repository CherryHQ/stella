package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/home"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
)

// fakeRemote is the canned remote MCP client behind mcp.Service's test-only
// connect hook. Real endpoints are unreachable in tests because the SSRF-safe
// dialer refuses loopback targets, so the transport seam is faked instead.
type fakeRemote struct {
	tools []*mcpsdk.Tool
}

func (c *fakeRemote) ListTools(context.Context) ([]*mcpsdk.Tool, error) {
	return c.tools, nil
}

func (c *fakeRemote) CallTool(context.Context, string, map[string]any) (*mcpsdk.CallToolResult, error) {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
}

func (c *fakeRemote) Close() error { return nil }

func setupFileMCPCatalogEnv(t *testing.T) *testEnv {
	return setupFileMCPCatalogEnvWithResource(t, true)
}

func setupFileMCPCatalogEnvWithResource(t *testing.T, seed bool) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	manager, err := home.NewWorkspaceManager(env.db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources := pluginpkg.NewResourceStore(manager)
	filePlugins := pluginpkg.NewFileService(resources, env.deps.AgentAccess)
	svc := mcp.NewServiceForPool(env.db, nil, nil)
	svc.SetFileConnectForTesting(func(context.Context, mcp.Registration, mcp.CredentialOwner, func()) (mcp.RemoteClient, error) {
		return &fakeRemote{tools: []*mcpsdk.Tool{{Name: "create_issue", Description: "Create an issue.", InputSchema: map[string]any{"type": "object"}}}}, nil
	})
	fileMCP := mcp.NewFileService(filePlugins, resources, svc)
	env.rebuild(t, func(d *server.Deps) {
		d.ToolOverrides = agent.NewToolOverrideStore(env.db, filePlugins)
		d.PluginFiles = filePlugins
		d.MCPFiles = fileMCP
		d.AgentMCPCatalog = func(ctx context.Context, authority authz.Authority, agentID string) ([]agent.MCPCatalogEntry, error) {
			entries, err := fileMCP.Catalog(ctx, authority, agentID)
			if err != nil {
				return nil, err
			}
			out := make([]agent.MCPCatalogEntry, 0, len(entries))
			for _, entry := range entries {
				out = append(out, agent.MCPCatalogEntry{
					Name: entry.Name, Description: entry.Description, InputSchema: entry.InputSchema,
					Identity: agent.ToolIdentity{PluginID: entry.PluginID, ServerKey: entry.ServerKey, LocalToolName: entry.LocalToolName},
					Family:   entry.Family,
				})
			}
			return out, nil
		}
		d.MCP = svc
	})
	if !seed {
		return env
	}
	key := pluginpkg.ResourceKey{Scope: pluginpkg.ScopeSystem, Kind: pluginpkg.ResourceMCP, Name: "github"}
	declaration := mcpconfig.Declaration{URL: "https://mcp.example.com", Transport: mcp.TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: mcp.AuthTypeNone, Mode: mcp.CredentialModeShared}}
	data, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.WriteMCP(t.Context(), key, "", data); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestAgentToolsListIncludesMCPCatalogEntries(t *testing.T) {
	env := setupFileMCPCatalogEnv(t)
	agentID := findStellaID(t, env)

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var got struct {
		Tools []struct {
			Name    string `json:"name"`
			Source  string `json:"source"`
			Family  string `json:"family"`
			Control string `json:"control"`
			Enabled *bool  `json:"enabled"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	var mcpTools []struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Family  string `json:"family"`
		Control string `json:"control"`
		Enabled *bool  `json:"enabled"`
	}
	for _, tool := range got.Tools {
		if tool.Source == "mcp" {
			mcpTools = append(mcpTools, tool)
		}
	}
	if len(mcpTools) != 1 {
		t.Fatalf("mcp tools = %#v, want exactly the cataloged tool", mcpTools)
	}
	tool := mcpTools[0]
	if tool.Name == "" || tool.Family != "mcp:github" || tool.Control != "override" {
		t.Fatalf("mcp tool = %#v", tool)
	}
	if tool.Enabled == nil || !*tool.Enabled {
		t.Fatalf("enabled = %v, want the default true", tool.Enabled)
	}
}

func TestAgentToolOverrideUsesUnifiedMCPIdentity(t *testing.T) {
	env := setupFileMCPCatalogEnv(t)
	agentID := findStellaID(t, env)
	initial := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial get tools status = %d", initial.Code)
	}
	var initialTools struct {
		Tools []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(initial.Body.Bytes(), &initialTools); err != nil {
		t.Fatal(err)
	}
	toolName := ""
	for _, item := range initialTools.Tools {
		if item.Source == "mcp" {
			toolName = item.Name
			break
		}
	}
	if toolName == "" {
		t.Fatalf("file MCP tool missing from initial list: %#v", initialTools.Tools)
	}

	// The unified registration carries a trusted plugin/local identity, so a
	// user-agent override is accepted and is keyed by that identity.
	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"enabled": false, "scope": "user_agent"})
	if rr.Code != http.StatusOK {
		t.Fatalf("unified user_agent patch status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get tools status = %d", rr.Code)
	}
	var got struct {
		Tools []struct {
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
			Origin  string `json:"origin"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	var tool struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
		Origin  string `json:"origin"`
	}
	for _, item := range got.Tools {
		if item.Name == toolName {
			tool = item
		}
	}
	if tool.Name == "" || tool.Enabled == nil {
		t.Fatalf("tool %q missing from list", toolName)
	}
	if *tool.Enabled || tool.Origin != "user_agent" {
		t.Fatalf("unified MCP decision = enabled %v origin %q, want false/user_agent", *tool.Enabled, tool.Origin)
	}

	// Even a stale legacy row cannot be interpreted as a core identity by the
	// runtime MCP path, which has no trusted identity to match.
	overrides := []agent.ToolOverride{
		{Identity: agent.ToolIdentity{}, Scope: agent.ToolOverrideScopeSystemAgent, Enabled: false},
		{Identity: agent.ToolIdentity{}, Scope: agent.ToolOverrideScopeUserAgent, Enabled: true},
	}
	if !agent.FilterToolEnabled(true, agent.ToolIdentity{}, overrides) {
		t.Fatal("FilterToolEnabled applied an empty-identity override")
	}
}

func TestAgentToolOverrideRejectsUnknownMCPName(t *testing.T) {
	env := setupFileMCPCatalogEnv(t)
	agentID := findStellaID(t, env)

	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/unknown-mcp-tool",
		map[string]any{"enabled": false, "scope": "user_agent"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown mcp tool status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

// A legacy per-user observation without a file declaration is not a catalog.
// Profile tools therefore expose no MCP tool and cannot create an override for
// an identity that the current file catalog does not own.
func TestAgentToolsPerUserNeedsAuth(t *testing.T) {
	env := setupFileMCPCatalogEnvWithResource(t, false)
	rr := doRequest(t, env, http.MethodGet, "/api/agents/stella/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var got struct {
		Tools []struct {
			Source string `json:"source"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	for _, item := range got.Tools {
		if item.Source == "mcp" {
			t.Fatalf("legacy observation leaked into file catalog: %#v", got.Tools)
		}
	}
}
