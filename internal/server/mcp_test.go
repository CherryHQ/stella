package server_test

import (
	"context"
	"encoding/json"
	"errors"
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

func TestLegacyMCPManagementRoutesRemoved(t *testing.T) {
	env := setupAdmin(t)
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/mcp/servers"},
		{http.MethodPost, "/api/mcp/servers"},
		{http.MethodGet, "/api/mcp/servers/example"},
		{http.MethodPatch, "/api/mcp/servers/example"},
		{http.MethodDelete, "/api/mcp/servers/example"},
		{http.MethodPost, "/api/mcp/servers/example/probe"},
		{http.MethodPost, "/api/mcp/servers/example/oauth-start"},
		{http.MethodPost, "/api/mcp/servers/example/oauth-disconnect"},
	}
	for _, route := range paths {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rr := doRequest(t, env, route.method, route.path, nil)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("legacy MCP route = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
			}
		})
	}
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
	svc.SetConnectForTesting(func(ctx context.Context, reg mcp.Registration, _ mcp.CredentialOwner) (mcp.RemoteClient, error) {
		connects++
		bearer, err := svc.BearerToken(ctx, reg)
		if err != nil {
			return nil, err
		}
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

func TestAgentToolOverrideRejectsUntrustedMCPIdentity(t *testing.T) {
	env, _ := setupMCPCatalogEnv(t)
	agentID := findStellaID(t, env)
	const toolName = "mcp__gh__create_issue"

	// The legacy registration has only a display name. A name-only PATCH must
	// fail closed until the MCP catalog supplies a trusted plugin/local pair.
	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"enabled": false, "scope": "system_agent"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("untrusted system_agent patch status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"enabled": true, "scope": "user_agent"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("untrusted user_agent patch status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
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
	if !*tool.Enabled || tool.Origin != agent.ToolOverrideOriginDefault {
		t.Fatalf("untrusted MCP decision = enabled %v origin %q, want true/default", *tool.Enabled, tool.Origin)
	}

	// Even a stale legacy row cannot be interpreted as a core identity by the
	// runtime MCP path, which has no trusted identity to match.
	overrides := []agent.ToolOverride{
		{Identity: agent.ToolIdentity{}, Scope: agent.ToolOverrideScopeSystemAgent, Enabled: false},
		{Identity: agent.ToolIdentity{}, Scope: agent.ToolOverrideScopeUserAgent, Enabled: true},
	}
	if !agent.FilterToolEnabled(true, agent.ToolIdentity{}, overrides) {
		t.Fatal("FilterToolEnabled applied a name-only legacy MCP override")
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

// TestAgentToolsPerUserNeedsAuth proves a per_user registration without the
// calling user's bundle lists its tools with availability_reason
// mcp_needs_auth even though the row's status may be shared/ok.
func TestAgentToolsPerUserNeedsAuth(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	q := sqlc.New(env.db)
	id := uuid.NewString()
	tools, err := json.Marshal([]mcp.CatalogTool{{
		Name: "search", Description: "Search.", InputSchema: map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMCPServer(ctx, sqlc.CreateMCPServerParams{
		ID: id, Scope: mcp.ScopeSystemAgent, AgentID: pgnull.Text("stella"),
		Name: "notion", Url: "https://mcp.example.com", Transport: mcp.TransportStreamableHTTP,
		AuthType: mcp.AuthTypeOAuth, Enabled: true, Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("seed registration: %v", err)
	}
	if _, err := env.db.Exec(ctx, `UPDATE mcp_server SET credential_mode = 'per_user' WHERE id = $1`, id); err != nil {
		t.Fatalf("set per_user: %v", err)
	}
	if _, err := q.UpdateMCPServerProbeResult(ctx, sqlc.UpdateMCPServerProbeResultParams{
		Status: mcp.StatusOK, ProbedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		Tools: tools, ID: id,
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	svc := mcp.NewServiceForPool(env.db, nil, nil)
	env.rebuild(t, func(d *server.Deps) {
		d.MCP = svc
		d.MCPAccess = mcp.NewAccess(svc, nil, nil)
	})

	rr := doRequest(t, env, http.MethodGet, "/api/agents/stella/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var got struct {
		Tools []struct {
			Name               string  `json:"name"`
			Enabled            *bool   `json:"enabled"`
			AvailabilityReason *string `json:"availability_reason"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	var tool struct {
		Name               string  `json:"name"`
		Enabled            *bool   `json:"enabled"`
		AvailabilityReason *string `json:"availability_reason"`
	}
	for _, item := range got.Tools {
		if item.Name == "mcp__notion__search" {
			tool = item
		}
	}
	if tool.Name == "" {
		t.Fatal("per_user tool missing from the list")
	}
	if tool.AvailabilityReason == nil || *tool.AvailabilityReason != "mcp_needs_auth" {
		t.Fatalf("availability_reason = %v, want mcp_needs_auth", tool.AvailabilityReason)
	}
	if tool.Enabled == nil || !*tool.Enabled {
		t.Fatalf("enabled = %v, want the default true (override stays editable)", tool.Enabled)
	}
}
