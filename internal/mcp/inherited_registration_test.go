package mcp

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestMCPListUsesInheritedDefinitionForEveryChild(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(t.Context(), authority)
	plugins, err := svc.plugins.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	_, parent, err := plugins.CreateCustom(ctx, plugin.Definition{ID: "inherited-mcp", DisplayName: "Inherited MCP", Spec: mustPublishedMCPTestSpec(`{"mcp_servers":{"main":{"url":"https://main.example.test","transport":"streamable_http","auth_type":"none"},"search":{"url":"https://search.example.test","transport":"streamable_http","auth_type":"none"}}}`)}, plugin.Config{Scope: plugin.ScopeUser, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.MCPServers) != 2 {
		t.Fatalf("children = %+v", parent.MCPServers)
	}
	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := access.List(ctx, ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("registrations = %+v", rows)
	}
	for _, row := range rows {
		if row.ParentConfigID != parent.ID || row.URL != "https://"+row.ServerKey+".example.test" {
			t.Fatalf("inherited registration = %+v", row)
		}
	}
}
