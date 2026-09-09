package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/config"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

type pluginProbeFixture struct {
	id string
}

func setupPluginProbeHTTPEnv(t *testing.T) (*fileMCPTestEnv, *[]mcp.CredentialOwner) {
	t.Helper()
	env := setupFileMCPTestEnv(t, mcp.EndpointPolicy{})
	seen := []mcp.CredentialOwner{}
	// ProbeFile is deliberately disposable. The hook lets this test assert
	// which per-user grant would be selected if a credential were available.
	env.mcpSvc.SetConnectForTesting(func(_ context.Context, _ mcp.Registration, owner mcp.CredentialOwner) (mcp.RemoteClient, error) {
		seen = append(seen, owner)
		return &fakeRemote{}, nil
	})
	return env, &seen
}

func installPluginProbeFixture(t *testing.T, env *fileMCPTestEnv, scope, userID, agentID, authType, credentialMode string) pluginProbeFixture {
	t.Helper()
	name := "probe-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	key := pluginpkg.ResourceKey{Scope: pluginpkg.Scope(scope), UserID: userID, AgentID: agentID, Kind: pluginpkg.ResourceMCP, Name: name}
	declaration := mcpconfig.Declaration{URL: "https://mcp.example.test", Transport: mcp.TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: authType, Mode: credentialMode}}
	data, err := json.Marshal(declaration)
	if err != nil {
		t.Fatalf("marshal MCP declaration: %v", err)
	}
	if _, err := env.resources.WriteMCP(context.Background(), key, "", data); err != nil {
		t.Fatalf("seed file MCP declaration: %v", err)
	}
	return pluginProbeFixture{id: fileMCPServerID(key, name)}
}

func pluginProbePath(f pluginProbeFixture) string {
	return fmt.Sprintf("/api/mcp/servers/%s/probe", f.id)
}

func TestPluginProbeHTTPAuthorizesParentAndBackend(t *testing.T) {
	env, _ := setupPluginProbeHTTPEnv(t)
	mcpFixture := installPluginProbeFixture(t, env, "system", "", "", mcp.AuthTypeNone, mcp.CredentialModeShared)
	if rr := doUnauthRequest(t, env.srv, http.MethodPost, pluginProbePath(mcpFixture), nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated probe status = %d, want 401 (body: %s)", rr.Code, rr.Body.String())
	}
	wrongChild := fmt.Sprintf("/api/mcp/servers/%s/probe", uuid.NewString())
	if rr := doRequest(t, env.testEnv, http.MethodPost, wrongChild, nil); rr.Code != http.StatusNotFound {
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

func TestPluginProbeHTTPUsesPerUserCredentialBoundary(t *testing.T) {
	env, owners := setupPluginProbeHTTPEnv(t)
	userA, tokenA := createTestUserWithToken(t, env.authStore, env.oidcStore, "probe-owner", "user")
	fixture := installPluginProbeFixture(t, env, mcp.ScopeUser, userA.ID, "", mcp.AuthTypeOAuth, mcp.CredentialModePerUser)
	rr := doRequestWithSession(t, env.srv, tokenA, http.MethodPost, pluginProbePath(fixture), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner probe status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"status":"needs_auth"`) {
		t.Fatalf("per-user probe did not report needs_auth: %s", rr.Body.String())
	}
	if len(*owners) != 0 {
		t.Fatalf("probe attempted a remote connection without the owner's grant: %#v", *owners)
	}
}
