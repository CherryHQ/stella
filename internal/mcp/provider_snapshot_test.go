package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestToolsForRegistrationsUsesPackageComponentTupleForExport(t *testing.T) {
	provider := NewToolProvider(NewService(newFakeDB(), nil))
	registration := Registration{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", PluginID: "remote.package",
		Scope: ScopeSystem, Enabled: true, Status: StatusOK, ProbedAt: time.Now().UTC(),
		Tools: []CatalogTool{{Name: "remote tool", Description: "remote"}},
	}

	tools := provider.toolsForRegistrations(t.Context(), []Registration{registration}, false, "user-1")
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	want, err := agentpackage.ExportedToolName(registration.PluginID, "main", "remote tool")
	if err != nil {
		t.Fatal(err)
	}
	if got := tools[0].Definition().Name; got != want {
		t.Fatalf("exported tool name = %q, want tuple projection %q", got, want)
	}
}

func TestToolsForRegistrationsSkipsLegacyRegistrationWithoutPackageID(t *testing.T) {
	provider := NewToolProvider(NewService(newFakeDB(), nil))
	registration := Registration{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", Name: "settings_server",
		Scope: ScopeSystem, Enabled: true, Status: StatusOK, ProbedAt: time.Now().UTC(),
		Tools: []CatalogTool{{Name: "list"}},
	}

	tools := provider.toolsForRegistrations(t.Context(), []Registration{registration}, false, "user-1")
	if len(tools) != 0 {
		t.Fatalf("tools = %d, want legacy registration without package ID to be skipped", len(tools))
	}
}

func TestToolProxyExposesDurablePluginIdentity(t *testing.T) {
	proxy := &toolProxy{reg: Registration{PluginID: "settings.server"}, remoteName: "list"}

	pluginID, local, ok := proxy.PluginToolIdentity()
	if !ok || pluginID != "settings.server" || local != "list" {
		t.Fatalf("PluginToolIdentity = %q, %q, %v; want settings.server, list, true", pluginID, local, ok)
	}
	legacy := &toolProxy{reg: Registration{Name: "settings_server"}, remoteName: "list"}
	if pluginID, local, ok := legacy.PluginToolIdentity(); ok || pluginID != "" || local != "" {
		t.Fatalf("legacy PluginToolIdentity = %q, %q, %v; want empty, empty, false", pluginID, local, ok)
	}
}

func TestToolsForRegistrationsSkipsDisabledWinner(t *testing.T) {
	provider := NewToolProvider(NewService(newFakeDB(), nil))
	registration := Registration{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", PluginID: "remote.package",
		Scope: ScopeSystem, Enabled: false, Status: StatusOK, ProbedAt: time.Now().UTC(),
		Tools: []CatalogTool{{Name: "remote tool"}},
	}

	if tools := provider.toolsForRegistrations(t.Context(), []Registration{registration}, false, "user-1"); len(tools) != 0 {
		t.Fatalf("tools = %d, want disabled winner to suppress exports", len(tools))
	}
}

func TestValidateCatalogToolsPreservesRawTupleAndRejectsExactCollision(t *testing.T) {
	reg := Registration{PluginID: "remote.package"}
	if err := validateCatalogTools(reg, []CatalogTool{{Name: "a.b"}, {Name: "a_b"}}); err != nil {
		t.Fatalf("distinct raw names collided: %v", err)
	}
	if err := validateCatalogTools(reg, []CatalogTool{{Name: "same"}, {Name: "same"}}); err == nil {
		t.Fatal("exact duplicate catalog name was accepted")
	}
	long := strings.Repeat("x", 64)
	if err := validateCatalogTools(reg, []CatalogTool{{Name: long}}); err != nil {
		t.Fatalf("long raw tool name should be truncated into a valid exported name: %v", err)
	}
}

func TestSnapshotMCPExportBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		payload   *string
		tools     []CatalogTool
		wantError string
		wantCount int
	}{
		{name: "negative only does not expose package", payload: nil},
		{name: "disabled malformed payload is not decoded", payload: func() *string { s := `{"url":17}`; return &s }()},
		{name: "exact remote names collide", enabled: true, payload: func() *string {
			s := `{"url":"https://mcp.example.test","transport":"streamable_http","auth_type":"none"}`
			return &s
		}(), tools: []CatalogTool{{Name: "same"}, {Name: "same"}}, wantError: "collide on exported tool name"},
		{name: "valid winner contributes", enabled: true, payload: func() *string {
			s := `{"url":"https://mcp.example.test","transport":"streamable_http","auth_type":"none"}`
			return &s
		}(), tools: []CatalogTool{{Name: "remote tool"}}, wantCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := dbtest.New(t)
			ctx := t.Context()
			const userID = "10000000-0000-0000-0000-000000000071"
			const configID = "20000000-0000-0000-0000-000000000071"
			const pluginID = "remote.package"
			if _, err := pool.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,$2)`, userID, "snapshot-boundary@test.invalid"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_definition(id,display_name,backend,source,implementation_key,spec,default_enabled,revision,creator_user_id) VALUES($1,'Remote','mcp','custom','mcp','{}',false,1,$2)`, pluginID, userID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_config(id,plugin_id,scope,user_id,enabled,config,credential_refs,revision) VALUES($1,$2,'user',$3,$4,$5,'{}',1)`, configID, pluginID, userID, tc.enabled, tc.payload); err != nil {
				t.Fatal(err)
			}
			authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
			if err != nil {
				t.Fatal(err)
			}
			svc := plugin.NewService(pool, nil, plugin.NewCatalog(), plugin.BackendPolicy{Transition: noopBackendTransition}, func(_ context.Context, fn func() error) error { return fn() })
			snapshot, err := svc.ResolveSnapshot(ctx, authority, "")
			if err != nil {
				t.Fatal(err)
			}
			observations := map[string]PluginMCPObservation{configID: {Status: StatusOK, ProbedAt: time.Now().UTC(), ConfigRevision: 1, Tools: tc.tools}}
			regs, err := mcpRegistrationsFromSnapshot(snapshot, observations, authority)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(regs) != tc.wantCount {
				t.Fatalf("registrations = %d, want %d", len(regs), tc.wantCount)
			}
		})
	}
}
