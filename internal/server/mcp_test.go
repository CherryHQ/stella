package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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

// setupMCPEnv wires a real mcp service (pool-backed) into a test server with a
// fake remote. The fake returns tools or an error depending on remoteErr.
func setupMCPEnv(t *testing.T, remoteErr error) (*testEnv, *int) {
	t.Helper()
	env := setupAdmin(t)
	connects := 0
	svc := mcp.NewServiceForPool(env.db, nil, nil)
	svc.SetConnectForTesting(func(context.Context, mcp.Registration, string) (mcp.RemoteClient, error) {
		connects++
		if remoteErr != nil {
			return nil, remoteErr
		}
		return &fakeRemote{tools: []*mcpsdk.Tool{
			{Name: "create_issue", Description: "Create issue", InputSchema: map[string]any{"type": "object"}},
		}}, nil
	})
	env.rebuild(t, func(d *server.Deps) {
		d.MCP = svc
		d.MCPAccess = mcp.NewAccess(svc, nil, nil)
	})
	return env, &connects
}

func createMCPServer(t *testing.T, env *testEnv) map[string]any {
	t.Helper()
	rr := doRequest(t, env, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "gh", "url": "https://mcp.example.com", "scope": "user",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return out
}

func TestMCPServerCreateProbesAndReturnsCatalog(t *testing.T) {
	env, connects := setupMCPEnv(t, nil)
	created := createMCPServer(t, env)

	if created["status"] != "ok" {
		t.Fatalf("create status = %v, want ok after the automatic probe", created["status"])
	}
	if *connects != 1 {
		t.Fatalf("connects = %d, want 1", *connects)
	}
	tools, ok := created["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one cataloged tool", created["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "create_issue" {
		t.Fatalf("tool = %#v", tool)
	}
	if created["version"] == "" {
		t.Fatal("version missing from response; web UI needs it for If-Match")
	}
	if created["credential_mode"] != "shared" {
		t.Fatalf("credential_mode = %v, want shared", created["credential_mode"])
	}
}

func TestMCPServerProbeEndpoint(t *testing.T) {
	t.Run("ok persists tools", func(t *testing.T) {
		env, _ := setupMCPEnv(t, nil)
		created := createMCPServer(t, env)
		id := created["id"].(string)

		rr := doRequest(t, env, http.MethodPost, fmt.Sprintf("/api/mcp/servers/%s/probe", id), nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("probe status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode probe response: %v", err)
		}
		if out["status"] != "ok" {
			t.Fatalf("probe status = %v, want ok", out["status"])
		}
		tools, ok := out["tools"].([]any)
		if !ok || len(tools) == 0 {
			t.Fatalf("probe tools = %#v, want non-empty", out["tools"])
		}
		if out["probed_at"] == nil {
			t.Fatal("probed_at missing")
		}
	})

	t.Run("failure returns 200 with status=error", func(t *testing.T) {
		env, _ := setupMCPEnv(t, errors.New("dial tcp: connection refused"))
		created := createMCPServer(t, env)
		id := created["id"].(string)

		rr := doRequest(t, env, http.MethodPost, fmt.Sprintf("/api/mcp/servers/%s/probe", id), nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("probe status = %d, want 200 even when the probe failed (body: %s)", rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode probe response: %v", err)
		}
		if out["status"] != "error" {
			t.Fatalf("probe status = %v, want error", out["status"])
		}
		errMsg, _ := out["status_error"].(string)
		if !strings.Contains(errMsg, "connection refused") {
			t.Fatalf("status_error = %q, want the failure reason", errMsg)
		}
	})
}

func TestMCPServerIfMatch(t *testing.T) {
	env, _ := setupMCPEnv(t, errors.New("dial tcp: connection refused"))
	created := createMCPServer(t, env)
	id := created["id"].(string)
	version, _ := created["version"].(string)
	if version == "" {
		t.Fatal("create response missing version")
	}

	t.Run("stale version conflicts", func(t *testing.T) {
		rr := doIfMatchRequest(t, env, http.MethodPatch, "/api/mcp/servers/"+id, `{"enabled":false}`, "stale-version")
		if rr.Code != http.StatusConflict {
			t.Fatalf("stale PATCH status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
		}
		rr = doIfMatchRequest(t, env, http.MethodDelete, "/api/mcp/servers/"+id, "", "stale-version")
		if rr.Code != http.StatusConflict {
			t.Fatalf("stale DELETE status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
		}
	})

	t.Run("matching version succeeds", func(t *testing.T) {
		rr := doIfMatchRequest(t, env, http.MethodPatch, "/api/mcp/servers/"+id, `{"enabled":false}`, version)
		if rr.Code != http.StatusOK {
			t.Fatalf("matching PATCH status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode patch response: %v", err)
		}
		if out["enabled"] != false {
			t.Fatalf("enabled = %v, want false", out["enabled"])
		}
		// DELETE with the pre-update version is now stale: the update bumped it.
		rr = doIfMatchRequest(t, env, http.MethodDelete, "/api/mcp/servers/"+id, "", version)
		if rr.Code != http.StatusConflict {
			t.Fatalf("post-update stale DELETE status = %d, want 409", rr.Code)
		}
		// Fetch the fresh version, then delete successfully.
		rr = doRequest(t, env, http.MethodGet, "/api/mcp/servers/"+id, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("get status = %d", rr.Code)
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode get response: %v", err)
		}
		freshVersion, _ := out["version"].(string)
		rr = doIfMatchRequest(t, env, http.MethodDelete, "/api/mcp/servers/"+id, "", freshVersion)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("matching DELETE status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
		}
	})
}

// doRequestWithHeaders sends an authenticated request with extra headers and
// an optional raw JSON body ("" for none).
func doRequestWithHeaders(t *testing.T, env *testEnv, method, path, body, header, headerValue string) *httptest.ResponseRecorder {
	t.Helper()
	var payload strings.Reader
	if body != "" {
		payload = *strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, &payload)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if headerValue != "" {
		req.Header.Set(header, headerValue)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: env.bearerToken})
	rr := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, req)
	return rr
}

func doIfMatchRequest(t *testing.T, env *testEnv, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithHeaders(t, env, method, path, body, "If-Match", ifMatch)
}

// A rotated bearer token is how a needs_auth server gets repaired, so PATCH
// with a new token must re-probe even though url/transport/auth_type are
// unchanged.
func TestMCPServerPatchTokenReprobes(t *testing.T) {
	env, vaultSvc := setupVaultEnv(t)
	connects := 0
	// bindVault stays nil: the test vault DB wrapper cannot join a pgx
	// transaction, and the token path under test does not depend on it.
	svc := mcp.NewServiceForPool(env.db, vaultSvc, nil)
	svc.SetConnectForTesting(func(_ context.Context, _ mcp.Registration, bearer string) (mcp.RemoteClient, error) {
		connects++
		if bearer != "good-token" {
			return nil, errors.New("initialize: Unauthorized")
		}
		return &fakeRemote{tools: []*mcpsdk.Tool{{Name: "ping", InputSchema: map[string]any{"type": "object"}}}}, nil
	})
	env.rebuild(t, func(d *server.Deps) {
		d.Vault = vaultSvc
		d.MCP = svc
		d.MCPAccess = mcp.NewAccess(svc, nil, nil)
	})

	rr := doRequest(t, env, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "guarded", "url": "https://mcp.example.com", "scope": "user", "auth_type": "bearer", "token": "bad-token",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created["status"] != "needs_auth" {
		t.Fatalf("create status = %v, want needs_auth for a rejected token", created["status"])
	}
	id, version := created["id"].(string), created["version"].(string)

	rr = doIfMatchRequest(t, env, http.MethodPatch, "/api/mcp/servers/"+id, `{"auth_type":"bearer","token":"good-token"}`, version)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var patched map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched["status"] != "ok" {
		t.Fatalf("patch status = %v, want ok after the token was replaced", patched["status"])
	}
	if connects != 2 {
		t.Fatalf("connects = %d, want 2 (create probe + token re-probe)", connects)
	}
	if strings.Contains(rr.Body.String(), "good-token") {
		t.Fatal("response echoed the bearer token")
	}
}

// seedCatalogedMCPServer inserts a user-scope registration whose persisted
// catalog lists one tool, so the profile tools endpoint can enumerate it
// without connecting anywhere.
func seedCatalogedMCPServer(t *testing.T, env *testEnv) mcp.Registration {
	t.Helper()
	ctx := context.Background()
	q := sqlc.New(env.db)
	id := uuid.NewString()
	tools, err := json.Marshal([]mcp.CatalogTool{{
		Name:        "create_issue",
		Description: "Create an issue.",
		InputSchema: map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMCPServer(ctx, sqlc.CreateMCPServerParams{
		ID: id, Scope: mcp.ScopeUser, UserID: pgnull.Text(env.adminUser.ID), Name: "gh", Url: "https://mcp.example.com",
		Transport: mcp.TransportStreamableHTTP, AuthType: mcp.AuthTypeNone, Enabled: true,
		Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("seed registration: %v", err)
	}
	if _, err := q.UpdateMCPServerProbeResult(ctx, sqlc.UpdateMCPServerProbeResultParams{
		Status: mcp.StatusOK, ProbedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		Tools: tools, ID: id,
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	reg, err := mcp.NewServiceForPool(env.db, nil, nil).Get(ctx, id, mcp.ScopeUser, env.adminUser.ID, "")
	if err != nil {
		t.Fatalf("read seeded registration: %v", err)
	}
	return reg
}

func setupMCPCatalogEnv(t *testing.T) (*testEnv, mcp.Registration) {
	t.Helper()
	env := setupAdmin(t)
	reg := seedCatalogedMCPServer(t, env)
	svc := mcp.NewServiceForPool(env.db, nil, nil)
	env.rebuild(t, func(d *server.Deps) {
		d.MCP = svc
		d.MCPAccess = mcp.NewAccess(svc, nil, nil)
	})
	return env, reg
}

func TestAgentToolsListIncludesMCPCatalogEntries(t *testing.T) {
	env, _ := setupMCPCatalogEnv(t)
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
	if tool.Name != "mcp__gh__create_issue" || tool.Family != "mcp:gh" || tool.Control != "override" {
		t.Fatalf("mcp tool = %#v", tool)
	}
	if tool.Enabled == nil || !*tool.Enabled {
		t.Fatalf("enabled = %v, want the default true", tool.Enabled)
	}
}

func TestAgentToolOverrideOnMCPTool(t *testing.T) {
	env, _ := setupMCPCatalogEnv(t)
	agentID := findStellaID(t, env)
	const toolName = "mcp__gh__create_issue"

	// Admin disables at the system_agent layer; a user_agent enable must lose.
	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"enabled": false, "scope": "system_agent"})
	if rr.Code != http.StatusOK {
		t.Fatalf("system_agent patch status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"enabled": true, "scope": "user_agent"})
	if rr.Code != http.StatusOK {
		t.Fatalf("user_agent patch status = %d (body: %s)", rr.Code, rr.Body.String())
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
	if *tool.Enabled || tool.Origin != agent.ToolOverrideScopeSystemAgent {
		t.Fatalf("override decision = enabled %v origin %q, want false/system_agent (admin disable wins)", *tool.Enabled, tool.Origin)
	}

	// Runner level: the same override rows the fetcher returns must hide the
	// tool from registration, not just from the profile list.
	overrides := []agent.ToolOverride{
		{ToolName: toolName, Scope: agent.ToolOverrideScopeSystemAgent, Enabled: false},
		{ToolName: toolName, Scope: agent.ToolOverrideScopeUserAgent, Enabled: true},
	}
	if agent.FilterToolEnabled(true, toolName, overrides) {
		t.Fatal("FilterToolEnabled registered an admin-disabled MCP tool")
	}
}

func TestAgentToolOverrideRejectsUnknownMCPName(t *testing.T) {
	env, _ := setupMCPCatalogEnv(t)
	agentID := findStellaID(t, env)

	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/mcp__gh__missing",
		map[string]any{"enabled": false, "scope": "user_agent"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown mcp tool status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}
