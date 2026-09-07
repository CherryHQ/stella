package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/config"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
)

// oauthTestVault is deliberately empty. The negative action cases stop at the
// MCP PEP before a credential is read or written; the per-user initialization
// case only needs bindVault to pass the metadata validation guard.
type oauthTestVault struct{}

func (oauthTestVault) SetScoped(context.Context, string, string, string, string, string) error {
	return nil
}

func (oauthTestVault) SetSystemScoped(context.Context, string, string, string, string) error {
	return nil
}

func (oauthTestVault) GetScoped(context.Context, string, string, string, string) (string, error) {
	return "", nil
}

func (oauthTestVault) DeleteScoped(context.Context, string, string, string, string) error {
	return nil
}

func (oauthTestVault) DeleteSystemScoped(context.Context, string, string, string) error {
	return nil
}

type pluginOAuthFixture struct {
	parentID string
	childID  string
}

func installPluginOAuthFixture(t *testing.T, env *testEnv, scope, userID, agentID, mode, endpoint string) pluginOAuthFixture {
	t.Helper()
	const pluginID = "oauth-test"
	configID := uuid.NewString()
	childID := uuid.NewString()
	spec, err := pluginpkg.PublishDefinitionSpec(json.RawMessage(fmt.Sprintf(`{"mcp_servers":{"main":{"url":%q,"transport":"streamable_http","auth_type":"oauth","credential_mode":%q}}}`, endpoint, mode)))
	if err != nil {
		t.Fatalf("publish definition: %v", err)
	}
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO plugin_definition(id, display_name, source, spec, default_enabled, revision)
		VALUES ($1, 'OAuth test', 'custom', $2::jsonb, false, 1)
		ON CONFLICT (id) DO NOTHING`, pluginID, spec); err != nil {
		t.Fatalf("seed plugin definition: %v", err)
	}
	payload := []byte(`{}`)
	bundle := map[string]any{
		"name": "MCP_OAUTH_" + strings.ToUpper(strings.ReplaceAll(childID, "-", "_")),
		"mode": mode,
	}
	refs := map[string]any{"mcp_servers": map[string]any{"main": map[string]any{"oauth_bundle": bundle}}}
	if mode == mcp.CredentialModePerUser {
		bundle["owner"] = "per_user"
	} else {
		bundle["scope"], bundle["user_id"], bundle["agent_id"] = scope, userID, agentID
	}
	credentialRefs, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("marshal OAuth refs: %v", err)
	}
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO plugin_config(id, plugin_id, scope, user_id, agent_id,
			enabled, config, credential_refs, revision)
		VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, ''),
			true, $6::jsonb, $7::jsonb, 1)`, configID, pluginID, scope, userID, agentID, payload, credentialRefs); err != nil {
		t.Fatalf("seed plugin config: %v", err)
	}
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO plugin_config_mcp_server(id, config_id, server_key)
		VALUES ($1::uuid, $2::uuid, 'main')`, childID, configID); err != nil {
		t.Fatalf("seed MCP child: %v", err)
	}
	return pluginOAuthFixture{parentID: configID, childID: childID}
}

func setupPluginOAuthHTTPEnv(t *testing.T, endpointPolicy mcp.EndpointPolicy) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	plugins := pluginpkg.NewService(env.db, env.deps.AgentAccess, pluginpkg.NewCatalog(), mcp.NewMCPBackendPolicy(endpointPolicy),
		func(_ context.Context, fn func() error) error { return fn() })
	mcpSvc := mcp.NewServiceForPool(env.db, oauthTestVault{}, func(pgx.Tx) mcp.Vault {
		return oauthTestVault{}
	})
	mcpSvc.SetEndpointPolicy(endpointPolicy)
	mcpSvc.SetPluginService(plugins)
	env.rebuild(t, func(d *server.Deps) {
		d.PluginService = plugins
		d.MCP = mcpSvc
		d.MCPAccess = mcp.NewAccess(mcpSvc, d.AgentAccess, nil)
	})
	return env
}

func TestPluginOAuthHTTPRejectsWrongParentAndUnknownConfig(t *testing.T) {
	env := setupPluginOAuthHTTPEnv(t, mcp.EndpointPolicy{})
	fixture := installPluginOAuthFixture(t, env, mcp.ScopeSystem, "", "", mcp.CredentialModeShared, "https://mcp.example.test/mcp")
	path := fmt.Sprintf("/api/mcp/servers/%s/oauth/start", fixture.childID)
	if rr := doUnauthRequest(t, env.srv, http.MethodPost, path, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated start status = %d, want 401 (body: %s)", rr.Code, rr.Body.String())
	}

	for _, action := range []string{"start", "disconnect"} {
		wrongParent := fmt.Sprintf("/api/mcp/servers/%s/oauth/%s", uuid.NewString(), action)
		if rr := doRequest(t, env, http.MethodPost, wrongParent, nil); rr.Code != http.StatusNotFound {
			t.Fatalf("wrong parent %s status = %d, want 404 (body: %s)", action, rr.Code, rr.Body.String())
		}
		unknownConfig := fmt.Sprintf("/api/mcp/servers/%s/oauth/%s", uuid.NewString(), action)
		if rr := doRequest(t, env, http.MethodPost, unknownConfig, nil); rr.Code != http.StatusNotFound {
			t.Fatalf("unknown config %s status = %d, want 404 (body: %s)", action, rr.Code, rr.Body.String())
		}
	}
}

func TestPluginOAuthHTTPRejectsUnauthorizedAgentAndSharedScope(t *testing.T) {
	env := setupPluginOAuthHTTPEnv(t, mcp.EndpointPolicy{})
	user, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "oauth-user", "user")
	agentID := "oauth-private-agent"
	if err := env.store.CreateAgent(context.Background(), config.Agent{
		ID: agentID, Name: "OAuth private", Model: "test/model",
		Scope: config.AgentScopeRestricted, CreatorID: "different-owner", Enabled: true,
	}); err != nil {
		t.Fatalf("seed private agent: %v", err)
	}
	userAgent := installPluginOAuthFixture(t, env, mcp.ScopeUserAgent, user.ID, agentID, mcp.CredentialModeShared, "https://mcp.example.test/mcp")
	systemAgent := installPluginOAuthFixture(t, env, mcp.ScopeSystemAgent, "", agentID, mcp.CredentialModeShared, "https://mcp.example.test/mcp")
	systemShared := installPluginOAuthFixture(t, env, mcp.ScopeSystem, "", "", mcp.CredentialModeShared, "https://mcp.example.test/mcp")

	for _, test := range []struct {
		name    string
		fixture pluginOAuthFixture
	}{
		{name: "user-agent", fixture: userAgent},
		{name: "system-agent", fixture: systemAgent},
		{name: "shared-system", fixture: systemShared},
	} {
		for _, action := range []string{"start", "disconnect"} {
			path := fmt.Sprintf("/api/mcp/servers/%s/oauth/%s", test.fixture.childID, action)
			if rr := doRequestWithSession(t, env.srv, userToken, http.MethodPost, path, nil); rr.Code != http.StatusForbidden {
				t.Fatalf("unauthorized %s %s status = %d, want 403 (body: %s)", test.name, action, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestPluginOAuthHTTPMapsSystemPerUserInitializationHint(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(remote.Close)
	env := setupPluginOAuthHTTPEnv(t, mcp.EndpointPolicy{AllowPrivate: true})
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "oauth-user", "user")
	fixture := installPluginOAuthFixture(t, env, mcp.ScopeSystem, "", "", mcp.CredentialModePerUser, remote.URL+"/mcp")
	path := fmt.Sprintf("/api/mcp/servers/%s/oauth/start", fixture.childID)
	rr := doRequestWithSession(t, env.srv, userToken, http.MethodPost, path, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("per-user initialization status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "administrator must initialize this connection before users can authorize their own accounts") {
		t.Fatalf("initialization hint missing from response: %s", rr.Body.String())
	}
}
