package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestFileMCPBearerTargetAndOwnerIsolation(t *testing.T) {
	svc, _, userA, _ := setupInternal(t)
	userB := newUserForOAuthTest(t, svc.pool)
	authorityA := fileSessionTestAuthority(t, userA)
	authorityB := fileSessionTestAuthority(t, userB)
	var calls atomic.Int32
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "file-auth", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "authorized"}}}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer personal-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	resource := plugin.FileResource{
		Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "private-account"},
		MCP: map[string]mcpconfig.Declaration{"main": {
			URL: endpoint.URL, Transport: TransportStreamableHTTP,
			Authentication: mcpconfig.Authentication{Type: AuthTypeBearer, Mode: CredentialModePerUser, CredentialRef: "FILE_TOKEN"},
		}},
	}
	reg, err := RegistrationFromFileResource(resource, "main", authorityA)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetFileBearerCredential(t.Context(), reg, authorityA, "personal-token"); err != nil {
		t.Fatal(err)
	}
	provider := NewToolProvider(svc)
	sessionA, sessionB := NewFileSession(svc), NewFileSession(svc)
	t.Cleanup(func() { _ = sessionA.Close(); _ = sessionB.Close() })
	toolsA, err := fileSessionTools(t, provider, t.Context(), sessionA, []Registration{reg}, authorityA)
	if err != nil || len(toolsA) != 1 {
		t.Fatalf("A tool discovery: %d tools, %v", len(toolsA), err)
	}
	if got, err := toolsA[0].Execute(authz.WithAuthority(t.Context(), authorityA), nil); err != nil || got != "authorized" {
		t.Fatalf("A call = %q, %v", got, err)
	}
	before := requests.Load()
	if toolsB, err := fileSessionTools(t, provider, t.Context(), sessionB, []Registration{reg}, authorityB); err != nil || len(toolsB) != 0 {
		t.Fatalf("B borrowed A authorization: %d tools, %v", len(toolsB), err)
	}
	if requests.Load() != before {
		t.Fatal("unauthorized B sent an MCP request")
	}
	resource.Digest = "edited-skill-and-scripts"
	unchanged, err := RegistrationFromFileResource(resource, "main", authorityA)
	if err != nil || unchanged.ID != reg.ID {
		t.Fatal("content-only edit changed authentication identity")
	}
	if err := sessionA.Prepare(t.Context(), nil, authorityA); err != nil {
		t.Fatal(err)
	}
	if restored, err := fileSessionTools(t, provider, t.Context(), sessionA, []Registration{unchanged}, authorityA); err != nil || len(restored) != 1 {
		t.Fatalf("restored declaration lost grant: %v", err)
	}
	var newTargetRequests atomic.Int32
	newTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		newTargetRequests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer newTarget.Close()
	declaration := resource.MCP["main"]
	declaration.URL = newTarget.URL
	resource.MCP["main"] = declaration
	changed, err := RegistrationFromFileResource(resource, "main", authorityA)
	if err != nil || changed.ID == reg.ID {
		t.Fatal("endpoint edit preserved old authentication identity")
	}
	if tools, err := fileSessionTools(t, provider, t.Context(), sessionA, []Registration{changed}, authorityA); err != nil || len(tools) != 0 {
		t.Fatalf("new endpoint reused referenced token: %d tools, %v", len(tools), err)
	}
	if newTargetRequests.Load() != 0 {
		t.Fatal("new endpoint received a request before authorization")
	}
	toolsA, err = fileSessionTools(t, provider, t.Context(), sessionA, []Registration{reg}, authorityA)
	if err != nil || len(toolsA) != 1 {
		t.Fatalf("original endpoint could not reuse grant: %v", err)
	}
	if err := svc.DisconnectFile(t.Context(), reg, authorityA); err != nil {
		t.Fatal(err)
	}
	before = requests.Load()
	if _, err := toolsA[0].Execute(t.Context(), nil); err == nil {
		t.Fatal("borrowed proxy survived explicit disconnect")
	}
	if requests.Load() != before || calls.Load() != 1 {
		t.Fatal("disconnected proxy sent a remote call")
	}
	if token, err := svc.vault.GetScoped(t.Context(), ScopeUser, userA, "", "FILE_TOKEN"); err != nil || token != "personal-token" {
		t.Fatal("disconnect removed the independently referenced Vault secret")
	}
}
