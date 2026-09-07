package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/config"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
)

type pluginProbeFixture struct {
	pluginID string
	configID string
	childID  string
}

func setupPluginProbeHTTPEnv(t *testing.T) (*testEnv, *[]mcp.CredentialOwner) {
	t.Helper()
	env := setupAdmin(t)
	plugins := pluginpkg.NewService(env.db, env.deps.AgentAccess, pluginpkg.NewCatalog(), mcp.NewMCPBackendPolicy(mcp.EndpointPolicy{}),
		func(_ context.Context, fn func() error) error { return fn() })
	owners := []mcp.CredentialOwner{}
	mcpSvc := mcp.NewServiceForPool(env.db, oauthTestVault{}, func(pgx.Tx) mcp.Vault {
		return oauthTestVault{}
	})
	mcpSvc.SetPluginService(plugins)
	mcpSvc.SetConnectForTesting(func(_ context.Context, _ mcp.Registration, owner mcp.CredentialOwner) (mcp.RemoteClient, error) {
		owners = append(owners, owner)
		return &fakeRemote{}, nil
	})
	env.rebuild(t, func(d *server.Deps) {
		d.PluginService = plugins
		d.MCP = mcpSvc
		d.MCPAccess = mcp.NewAccess(mcpSvc, d.AgentAccess, nil)
	})
	return env, &owners
}

func installPluginProbeFixture(t *testing.T, env *testEnv, scope, userID, agentID, authType, credentialMode string) pluginProbeFixture {
	t.Helper()
	name := "probe-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	pluginID := name
	configID := uuid.NewString()
	childID := uuid.NewString()
	child := fmt.Sprintf(`{"url":"https://mcp.example.test","transport":"streamable_http","auth_type":%q,"credential_mode":%q}`, authType, credentialMode)
	spec, err := pluginpkg.PublishDefinitionSpec(json.RawMessage(fmt.Sprintf(`{"mcp_servers":{"main":%s}}`, child)))
	if err != nil {
		t.Fatalf("publish definition: %v", err)
	}
	refs := `{}`
	if authType == mcp.AuthTypeOAuth && credentialMode == mcp.CredentialModePerUser {
		refs = fmt.Sprintf(`{"mcp_servers":{"main":{"oauth_bundle":{"name":"MCP_OAUTH_%s","mode":"per_user","owner":"per_user"}}}}`, strings.ToUpper(strings.ReplaceAll(childID, "-", "_")))
	}
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO plugin_definition(id, display_name, source, spec, default_enabled, revision)
		VALUES ($1, 'Probe test', 'custom', $2::jsonb, false, 1)`, pluginID, spec); err != nil {
		t.Fatalf("seed plugin definition: %v", err)
	}
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO plugin_config(id, plugin_id, scope, user_id, agent_id,
			enabled, config, credential_refs, revision)
		VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, ''),
			$6, $7::jsonb, $8::jsonb, 1)`, configID, pluginID, scope, userID, agentID, true, `{}`, refs); err != nil {
		t.Fatalf("seed plugin config: %v", err)
	}
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO plugin_config_mcp_server(id, config_id, server_key)
		VALUES ($1::uuid, $2::uuid, 'main')`, childID, configID); err != nil {
		t.Fatalf("seed MCP child: %v", err)
	}
	return pluginProbeFixture{pluginID: pluginID, configID: configID, childID: childID}
}

func pluginProbePath(f pluginProbeFixture) string {
	return fmt.Sprintf("/api/mcp/servers/%s/probe", f.childID)
}

func TestPluginProbeHTTPAuthorizesParentAndBackend(t *testing.T) {
	env, _ := setupPluginProbeHTTPEnv(t)
	mcpFixture := installPluginProbeFixture(t, env, "system", "", "", mcp.AuthTypeNone, mcp.CredentialModeShared)
	if rr := doUnauthRequest(t, env.srv, http.MethodPost, pluginProbePath(mcpFixture), nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated probe status = %d, want 401 (body: %s)", rr.Code, rr.Body.String())
	}
	wrongChild := fmt.Sprintf("/api/mcp/servers/%s/probe", uuid.NewString())
	if rr := doRequest(t, env, http.MethodPost, wrongChild, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown child probe status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestPluginProbeHTTPReturnsAgentPEPForbidden(t *testing.T) {
	env, _ := setupPluginProbeHTTPEnv(t)
	user, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "probe-user", "user")
	agentID := "probe-private-agent"
	if err := env.store.CreateAgent(context.Background(), config.Agent{ID: agentID, Name: "Private", Model: "test/model", Scope: config.AgentScopeRestricted, CreatorID: "another-owner", Enabled: true}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	fixture := installPluginProbeFixture(t, env, mcp.ScopeUserAgent, user.ID, agentID, mcp.AuthTypeNone, mcp.CredentialModeShared)
	if rr := doRequestWithSession(t, env.srv, token, http.MethodPost, pluginProbePath(fixture), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unauthorized agent probe status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestPluginProbeHTTPUsesOwnPerUserObservation(t *testing.T) {
	env, _ := setupPluginProbeHTTPEnv(t)
	userA, tokenA := createTestUserWithToken(t, env.authStore, env.oidcStore, "probe-owner", "user")
	userB, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "probe-other", "user")
	fixture := installPluginProbeFixture(t, env, mcp.ScopeUser, userA.ID, "", mcp.AuthTypeOAuth, mcp.CredentialModePerUser)
	if _, err := env.db.Exec(context.Background(), `
		INSERT INTO mcp_connection_state(child_id, credential_user_id, tools, status, status_error, config_revision)
		VALUES ($1::uuid, $2::uuid, '[]'::jsonb, 'error', 'other-user', 1)`, fixture.childID, userB.ID); err != nil {
		t.Fatalf("seed other-user observation: %v", err)
	}
	if rr := doRequestWithSession(t, env.srv, tokenA, http.MethodPost, pluginProbePath(fixture), nil); rr.Code != http.StatusOK {
		t.Fatalf("owner probe status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	} else if strings.Contains(rr.Body.String(), "mcp.example.test") {
		t.Fatalf("probe response exposed MCP endpoint: %s", rr.Body.String())
	}
	var ownerStatus, otherStatus, otherError string
	if err := env.db.QueryRow(context.Background(), `SELECT status FROM mcp_connection_state WHERE child_id = $1::uuid AND credential_user_id = $2::uuid`, fixture.childID, userA.ID).Scan(&ownerStatus); err != nil {
		t.Fatalf("read owner observation: %v", err)
	}
	if err := env.db.QueryRow(context.Background(), `SELECT status, status_error FROM mcp_connection_state WHERE child_id = $1::uuid AND credential_user_id = $2::uuid`, fixture.childID, userB.ID).Scan(&otherStatus, &otherError); err != nil {
		t.Fatalf("read other observation: %v", err)
	}
	if ownerStatus != mcp.StatusNeedsAuth {
		t.Fatalf("owner observation status = %q, want %q", ownerStatus, mcp.StatusNeedsAuth)
	}
	if otherStatus != mcp.StatusError || otherError != "other-user" {
		t.Fatalf("other observation changed to %q/%q", otherStatus, otherError)
	}
}
