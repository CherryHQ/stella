package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/internal/vault"
)

func setupPluginMutationHTTP(t *testing.T) (*testEnv, *vault.Service) {
	t.Helper()
	env := setupAdmin(t)
	master, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := vault.NewServiceForPool(env.db, master.String(), env.deps.AgentAccess)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := vault.GenerateUserKeys(secrets.MasterRecipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := env.oidcStore.UpdateUserAgeKeys(t.Context(), env.adminUser.ID, public, private); err != nil {
		t.Fatal(err)
	}
	policy := mcp.EndpointPolicy{}
	plugins := pluginpkg.NewService(env.db, env.deps.AgentAccess, pluginpkg.NewCatalog(), mcp.NewMCPBackendPolicy(policy), func(_ context.Context, fn func() error) error { return fn() })
	backend := mcp.NewServiceForPool(env.db, secrets, func(tx pgx.Tx) mcp.Vault { return secrets.WithTx(tx) })
	backend.SetEndpointPolicy(policy)
	backend.SetPluginService(plugins)
	env.rebuild(t, func(d *server.Deps) {
		d.PluginService = plugins
		d.MCP = backend
		d.MCPAccess = mcp.NewAccess(backend, d.AgentAccess, nil)
	})
	return env, secrets
}

func TestPluginMCPHTTPAtomicCredentialsAndClosedProjection(t *testing.T) {
	env, secrets := setupPluginMutationHTTP(t)
	body := map[string]any{
		"name": "http-mcp", "display_name": "HTTP MCP", "definition_spec": map[string]any{},
		"initial_config": map[string]any{"scope": "user", "is_enabled": true, "config": map[string]any{"url": "https://mcp.example.test/path/endpoint-secret", "auth_type": "bearer", "transport": "streamable_http"}, "credentials": map[string]any{"token": "first-bearer-secret"}},
	}
	if rr := doUnauthRequest(t, env.srv, http.MethodPost, "/api/plugins", body); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", rr.Code)
	}
	rr := doRequest(t, env, http.MethodPost, "/api/plugins", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rr.Code, rr.Body.String())
	}
	for _, value := range []string{"first-bearer-secret", "endpoint-secret", "https://mcp.example.test", "credential_refs"} {
		if strings.Contains(rr.Body.String(), value) {
			t.Fatalf("response exposed %q", value)
		}
	}
	var created apitypes.CreatePluginResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	cfg := created.Config
	if len(cfg.ResourceSummary.McpServers) != 1 || cfg.ResourceSummary.McpServers[0].ChildId == nil {
		t.Fatalf("created MCP child summary = %#v", cfg.ResourceSummary.McpServers)
	}
	name := "MCP_TOKEN_" + strings.ToUpper(strings.ReplaceAll(cfg.ResourceSummary.McpServers[0].ChildId.String(), "-", "_"))
	if got, err := secrets.GetScoped(t.Context(), "user", env.adminUser.ID, "", name); err != nil || got != "first-bearer-secret" {
		t.Fatalf("stored initial credential: %v", err)
	}
	parentRevision := *cfg.Revision
	for _, serverKey := range []string{"child-alpha", "child-beta"} {
		response := doRequest(t, env, http.MethodPost, "/api/mcp/servers", map[string]any{
			"parent_config_id":         cfg.Id.String(),
			"server_key":               serverKey,
			"expected_parent_revision": parentRevision,
			"url":                      "https://mcp.example.test",
			"auth_type":                "none",
			"transport":                "streamable_http",
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("child %s=%d %s", serverKey, response.Code, response.Body.String())
		}
		var child apitypes.MCPServer
		if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
			t.Fatal(err)
		}
		if child.Enabled == nil || !*child.Enabled || child.ParentRevision == nil || *child.ParentRevision <= parentRevision {
			t.Fatalf("child projection = %#v", child)
		}
		parentRevision = *child.ParentRevision
	}
	mainChild := cfg.ResourceSummary.McpServers[0].ChildId.String()
	path := "/api/mcp/servers/" + mainChild
	update := map[string]any{"expected_parent_revision": parentRevision, "url": "https://changed.example.test/mcp", "credentials": map[string]any{"token": "second-bearer-secret"}}
	wrong := "/api/mcp/servers/" + uuid.NewString()
	if rr := doRequest(t, env, http.MethodPatch, wrong, update); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown child=%d %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, path, update)
	if rr.Code != http.StatusOK {
		t.Fatalf("replace=%d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "second-bearer-secret") {
		t.Fatal("replacement leaked")
	}
	if got, err := secrets.GetScoped(t.Context(), "user", env.adminUser.ID, "", name); err != nil || got != "second-bearer-secret" {
		t.Fatalf("replacement credential: %v", err)
	}
	if rr := doRequest(t, env, http.MethodPatch, path, update); rr.Code != http.StatusConflict {
		t.Fatalf("stale=%d %s", rr.Code, rr.Body.String())
	}
	if got, err := secrets.GetScoped(t.Context(), "user", env.adminUser.ID, "", name); err != nil || got != "second-bearer-secret" {
		t.Fatalf("child credential changed unexpectedly: %v", err)
	}
}

func TestPluginMCPHTTPRejectsRawLocatorsAndForeignScope(t *testing.T) {
	env, _ := setupPluginMutationHTTP(t)
	_, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "plugin-http-user", "user")
	body := map[string]any{"name": "forbidden-mcp", "display_name": "Forbidden MCP", "definition_spec": map[string]any{}, "initial_config": map[string]any{"scope": "system", "config": map[string]any{"url": "https://mcp.example.test", "auth_type": "none", "transport": "streamable_http"}}}
	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/plugins", body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign scope=%d %s", rr.Code, rr.Body.String())
	}
	body["initial_config"].(map[string]any)["credential_refs"] = map[string]any{"bearer": map[string]any{"name": "foreign"}}
	rr = doRequest(t, env, http.MethodPost, "/api/plugins", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("raw locator=%d %s", rr.Code, rr.Body.String())
	}
	var rows int
	if err := env.db.QueryRow(t.Context(), `SELECT count(*) FROM plugin_definition WHERE id='forbidden-mcp'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("rejected creation persisted definition")
	}
}

func TestPluginMCPHTTPInvalidBackendInput(t *testing.T) {
	env, _ := setupPluginMutationHTTP(t)
	for _, field := range []string{"transport", "auth_type", "credential_mode"} {
		t.Run(field, func(t *testing.T) {
			cfg := map[string]any{"url": "https://mcp.example.test/private-path", "auth_type": "none", "transport": "streamable_http"}
			cfg[field] = "invalid-secret-value"
			body := map[string]any{"name": "invalid-mcp", "display_name": "Invalid MCP", "definition_spec": map[string]any{}, "initial_config": map[string]any{"scope": "user", "is_enabled": true, "config": cfg}}
			rr := doRequest(t, env, http.MethodPost, "/api/plugins", body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			for _, secret := range []string{"private-path", "invalid-secret-value"} {
				if strings.Contains(rr.Body.String(), secret) {
					t.Fatal("error exposed input")
				}
			}
		})
	}
}

func TestPluginHTTPDefinitionLifecycleAndConfigIsolation(t *testing.T) {
	env, secrets := setupPluginMutationHTTP(t)
	create := func(name, displayName, endpoint string) apitypes.CreatePluginResponse {
		t.Helper()
		rr := doRequest(t, env, http.MethodPost, "/api/plugins", map[string]any{
			"name": name, "display_name": displayName,
			"definition_spec": map[string]any{},
			"initial_config": map[string]any{
				"scope": "user", "is_enabled": true,
				"config":      map[string]any{"url": endpoint, "auth_type": "bearer", "transport": "streamable_http"},
				"credentials": map[string]any{"token": name + "-token"},
			},
		})
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", name, rr.Code, rr.Body.String())
		}
		var created apitypes.CreatePluginResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode create %s: %v", name, err)
		}
		if created.Plugin.Id != name || created.Plugin.DisplayName != displayName {
			t.Fatalf("created plugin = %#v", created.Plugin)
		}
		return created
	}

	first := create("lifecycle-a", "Lifecycle A", "https://a.example.test/mcp")
	second := create("lifecycle-b", "Lifecycle B", "https://b.example.test/mcp")
	var before []byte
	if err := env.db.QueryRow(t.Context(), `SELECT config FROM plugin_config WHERE id = $1`, second.Config.Id.String()).Scan(&before); err != nil {
		t.Fatalf("read second config: %v", err)
	}
	if len(first.Config.ResourceSummary.McpServers) != 1 || first.Config.ResourceSummary.McpServers[0].ChildId == nil {
		t.Fatalf("first MCP child summary = %#v", first.Config.ResourceSummary.McpServers)
	}
	updatePath := "/api/mcp/servers/" + first.Config.ResourceSummary.McpServers[0].ChildId.String()
	update := map[string]any{
		"expected_parent_revision": first.Config.Revision,
		"url":                      "https://a-updated.example.test/mcp",
		"credentials":              map[string]any{"token": "lifecycle_a-replacement"},
	}
	rr := doRequest(t, env, http.MethodPatch, updatePath, update)
	if rr.Code != http.StatusOK {
		t.Fatalf("update first = %d: %s", rr.Code, rr.Body.String())
	}
	var after []byte
	if err := env.db.QueryRow(t.Context(), `SELECT config FROM plugin_config WHERE id = $1`, second.Config.Id.String()).Scan(&after); err != nil {
		t.Fatalf("read second config after first update: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("updating one plugin changed another config: before=%s after=%s", before, after)
	}

	catalog := pluginpkg.NewCatalog()
	builtinSpec, err := pluginpkg.PublishDefinitionSpec(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(pluginpkg.Definition{
		ID: "builtin.lifecycle", DisplayName: "Builtin lifecycle",
		Source: pluginpkg.SourceBuiltin,
		Spec:   builtinSpec, DefaultEnabled: true, Revision: 1,
	}); err != nil {
		t.Fatalf("register builtin: %v", err)
	}
	plugins := pluginpkg.NewService(env.db, env.deps.AgentAccess, catalog, mcp.NewMCPBackendPolicy(mcp.EndpointPolicy{}), func(_ context.Context, fn func() error) error { return fn() })
	backend := mcp.NewServiceForPool(env.db, secrets, func(tx pgx.Tx) mcp.Vault { return secrets.WithTx(tx) })
	backend.SetPluginService(plugins)
	env.rebuild(t, func(d *server.Deps) {
		d.PluginService = plugins
		d.MCP = backend
		d.MCPAccess = mcp.NewAccess(backend, d.AgentAccess, nil)
	})
	if err := plugins.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("sync builtin: %v", err)
	}
	builtinDelete := doRequest(t, env, http.MethodDelete, pluginAPIPath("builtin.lifecycle")+"?expected_revision=1", nil)
	if builtinDelete.Code != http.StatusForbidden {
		t.Fatalf("builtin delete = %d, want 403: %s", builtinDelete.Code, builtinDelete.Body.String())
	}
	var builtinCount int
	if err := env.db.QueryRow(t.Context(), `SELECT count(*) FROM plugin_definition WHERE id = 'builtin.lifecycle'`).Scan(&builtinCount); err != nil {
		t.Fatal(err)
	}
	if builtinCount != 1 {
		t.Fatal("builtin definition was deleted")
	}

	var secondRevision int64
	if err := env.db.QueryRow(t.Context(), `SELECT revision FROM plugin_config WHERE id = $1`, second.Config.Id.String()).Scan(&secondRevision); err != nil {
		t.Fatalf("read second revision: %v", err)
	}
	staleDelete := doRequest(t, env, http.MethodDelete, pluginAPIPath(second.Plugin.Id)+"?expected_revision=2", nil)
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("stale custom delete = %d, want 409: %s", staleDelete.Code, staleDelete.Body.String())
	}
	deleteConfig := doRequest(t, env, http.MethodDelete, pluginAPIPath(second.Plugin.Id)+"/configs/"+second.Config.Id.String()+"?expected_revision="+strconv.FormatInt(secondRevision, 10), nil)
	if deleteConfig.Code != http.StatusNoContent {
		t.Fatalf("custom config delete = %d, want 204: %s", deleteConfig.Code, deleteConfig.Body.String())
	}
	deleteSecond := doRequest(t, env, http.MethodDelete, pluginAPIPath(second.Plugin.Id)+"?expected_revision=1", nil)
	if deleteSecond.Code != http.StatusNoContent {
		t.Fatalf("custom delete = %d, want 204: %s", deleteSecond.Code, deleteSecond.Body.String())
	}
}
