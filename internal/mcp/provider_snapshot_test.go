package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func TestToolsForRegistrationsUsesPackageComponentTupleForExport(t *testing.T) {
	provider := NewToolProvider(NewService(newFakeDB(), nil))
	registration := Registration{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", PluginID: "remote.package", ServerKey: "main",
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

func TestMCPDirectoryKeepsChildReadinessAndExactToolDeclaration(t *testing.T) {
	regs := []Registration{
		{PluginID: "remote", ParentConfigID: "cfg", ServerKey: "healthy", Scope: ScopeSystem, ConfigRevision: 4, Enabled: true, Status: StatusOK, Tools: []CatalogTool{{Name: "search", Description: "search", InputSchema: map[string]any{"type": "object"}, Annotations: map[string]any{"readOnlyHint": true}}}},
		{PluginID: "remote", ParentConfigID: "cfg", ServerKey: "broken", Scope: ScopeSystem, ConfigRevision: 4, Enabled: true, Status: StatusNeedsAuth, StatusError: "reauth required"},
	}
	search, err := agentpackage.ExportedToolName("remote", "healthy", "search")
	if err != nil {
		t.Fatal(err)
	}
	tools := []pkgtools.Tool{testDefinitionTool{definition: pkgtools.Definition{Name: search, Description: "search", InputSchema: map[string]any{"type": "object"}}}}
	entries := mcpDirectory(regs, []registrationToolsResult{{tools: tools, ready: true, status: StatusOK}, {ready: false, status: StatusNeedsAuth, statusError: "reauth required"}})
	if len(entries) != 2 {
		t.Fatalf("directory readiness = %#v", entries)
	}
	var healthy, broken *pkgplugins.MCPDirectoryEntry
	for i := range entries {
		switch entries[i].ServerKey {
		case "healthy":
			healthy = &entries[i]
		case "broken":
			broken = &entries[i]
		}
	}
	if healthy == nil || broken == nil || !healthy.Ready || broken.Ready || broken.StatusError == "" {
		t.Fatalf("directory readiness = %#v", entries)
	}
	if len(healthy.Tools) != 1 || healthy.Tools[0].Name != search || healthy.Tools[0].Annotations["readOnlyHint"] != true {
		t.Fatalf("directory tool declaration = %#v", healthy.Tools)
	}
	if got := successfulPluginIDs(regs, []registrationToolsResult{{tools: tools, ready: true}, {ready: false}}); len(got) != 0 {
		t.Fatalf("partial child success marked package ready: %v", got)
	}
}

type testDefinitionTool struct{ definition pkgtools.Definition }

func (t testDefinitionTool) Definition() pkgtools.Definition                       { return t.definition }
func (testDefinitionTool) Execute(context.Context, map[string]any) (string, error) { return "", nil }

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
	proxy := &toolProxy{reg: Registration{PluginID: "settings.server", ServerKey: "search"}, remoteName: "list"}

	pluginID, serverKey, local, ok := proxy.PluginToolIdentity()
	if !ok || pluginID != "settings.server" || serverKey != "search" || local != "list" {
		t.Fatalf("PluginToolIdentity = %q, %q, %v; want settings.server, list, true", pluginID, local, ok)
	}
	legacy := &toolProxy{reg: Registration{Name: "settings_server"}, remoteName: "list"}
	if pluginID, serverKey, local, ok := legacy.PluginToolIdentity(); ok || pluginID != "" || serverKey != "" || local != "" {
		t.Fatalf("legacy PluginToolIdentity = %q, %q, %v; want empty, empty, false", pluginID, local, ok)
	}
}

func TestToolsForRegistrationsSkipsDisabledWinner(t *testing.T) {
	provider := NewToolProvider(NewService(newFakeDB(), nil))
	registration := Registration{
		ID: "0198f9a4-1b2c-7def-8123-456789abcdef", PluginID: "remote.package", ServerKey: "main",
		Scope: ScopeSystem, Enabled: false, Status: StatusOK, ProbedAt: time.Now().UTC(),
		Tools: []CatalogTool{{Name: "remote tool"}},
	}

	if tools := provider.toolsForRegistrations(t.Context(), []Registration{registration}, false, "user-1"); len(tools) != 0 {
		t.Fatalf("tools = %d, want disabled winner to suppress exports", len(tools))
	}
}

func TestValidateCatalogToolsPreservesRawTupleAndRejectsExactCollision(t *testing.T) {
	reg := Registration{PluginID: "remote.package", ServerKey: "main"}
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
			const childID = "30000000-0000-0000-0000-000000000071"
			const pluginID = "remote.package"
			if _, err := pool.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,$2)`, userID, "snapshot-boundary@test.invalid"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_definition(id,display_name,source,spec,default_enabled,revision,creator_user_id) VALUES($1,'Remote','custom',$3::jsonb,false,1,$2)`, pluginID, userID, mustPublishedMCPTestSpec(`{}`)); err != nil {
				t.Fatal(err)
			}
			if tc.payload != nil {
				nested := `{"mcp_servers":{"main":` + *tc.payload + `}}`
				tc.payload = &nested
			}
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_config(id,plugin_id,scope,user_id,enabled,config,credential_refs,revision) VALUES($1,$2,'user',$3,$4,$5,'{}',1)`, configID, pluginID, userID, tc.enabled, tc.payload); err != nil {
				t.Fatal(err)
			}
			if tc.payload != nil {
				if _, err := pool.Exec(ctx, `INSERT INTO plugin_config_mcp_server(id,config_id,server_key) VALUES($1,$2,'main')`, childID, configID); err != nil {
					t.Fatal(err)
				}
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
			observations := map[string]PluginMCPObservation{childID: {Status: StatusOK, ProbedAt: time.Now().UTC(), ConfigRevision: 1, Tools: tc.tools}}
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

func TestMCPChildrenWithSameToolNameRemainIndependent(t *testing.T) {
	provider := NewToolProvider(NewService(newFakeDB(), nil))
	regs := []Registration{
		{ID: "10000000-0000-0000-0000-000000000001", PluginID: "demo", ServerKey: "alpha", Scope: ScopeSystem, Enabled: true, Status: StatusOK, ProbedAt: time.Now().UTC(), Tools: []CatalogTool{{Name: "search"}}},
		{ID: "10000000-0000-0000-0000-000000000002", PluginID: "demo", ServerKey: "beta", Scope: ScopeSystem, Enabled: true, Status: StatusOK, ProbedAt: time.Now().UTC(), Tools: []CatalogTool{{Name: "search"}}},
	}
	got := provider.toolsForRegistrations(t.Context(), regs, false, "")
	if len(got) != 2 {
		t.Fatalf("same-named sibling tools: got %d, want 2", len(got))
	}
	if got[0].Definition().Name == got[1].Definition().Name {
		t.Fatal("server key was lost from exported identity")
	}
}

func TestMCPMalformedChildDoesNotHideHealthySibling(t *testing.T) {
	const parentID = "10000000-0000-0000-0000-000000000001"
	const healthyID = "10000000-0000-0000-0000-000000000002"
	const badID = "10000000-0000-0000-0000-000000000003"
	def := plugin.Definition{ID: "demo", DisplayName: "Demo", Source: plugin.SourceCustom, Spec: json.RawMessage(mustPublishedMCPTestSpec(`{}`)), Revision: 1}
	payload := json.RawMessage(`{"mcp_servers":{"healthy":{"url":"https://example.com/mcp","transport":"streamable_http","auth_type":"none"},"bad":{"url":17}}}`)
	cfg := plugin.Config{ID: parentID, PluginID: def.ID, Scope: plugin.ScopeSystem, Enabled: boolPtr(true), Payload: payload, CredentialRefs: json.RawMessage(`{}`), Revision: 1, MCPServers: []plugin.MCPServerChild{{ID: healthyID, ParentConfigID: parentID, ServerKey: "healthy"}, {ID: badID, ParentConfigID: parentID, ServerKey: "bad"}}}
	effective := plugin.Effective{PluginID: def.ID, ConfigID: parentID, SourceScope: cfg.Scope, IsEffectivelyEnabled: true, Payload: payload}
	got, err := registrationsFromResolvedConfig(def, cfg, effective, PluginMCPObservation{}, nil, testUserAuthority(t, "user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != healthyID {
		t.Fatalf("healthy sibling lost: %+v", got)
	}
}

func TestMCPEmptyResourceSetDoesNotFailDiscovery(t *testing.T) {
	cfg := plugin.Config{Payload: json.RawMessage(`{"mcp_servers":{}}`)}
	got, err := registrationsFromResolvedConfig(plugin.Definition{}, cfg, plugin.Effective{Payload: cfg.Payload}, PluginMCPObservation{}, nil, testUserAuthority(t, "user-1"))
	if err != nil || len(got) != 0 {
		t.Fatalf("empty resource set: got %v, %v", got, err)
	}
}
