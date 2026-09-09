package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
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

type fileMCPTestEnv struct {
	*testEnv
	resources *pluginpkg.ResourceStore
	mcpSvc    *mcp.Service
}

func setupFileMCPTestEnv(t *testing.T, endpointPolicy mcp.EndpointPolicy) *fileMCPTestEnv {
	t.Helper()
	return wireFileMCPServices(t, setupAdmin(t), endpointPolicy)
}

func wireFileMCPServices(t *testing.T, env *testEnv, endpointPolicy mcp.EndpointPolicy) *fileMCPTestEnv {
	t.Helper()
	if err := os.MkdirAll(config.StellaHome(), 0o755); err != nil {
		t.Fatalf("create STELLA_HOME: %v", err)
	}
	manager, err := home.NewWorkspaceManager(env.db, config.StellaHome())
	if err != nil {
		t.Fatalf("home.NewWorkspaceManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources := pluginpkg.NewResourceStore(manager)
	files := pluginpkg.NewFileService(resources, env.deps.AgentAccess)
	mcpSvc := mcp.NewServiceForPool(env.db, oauthTestVault{}, func(pgx.Tx) mcp.Vault {
		return oauthTestVault{}
	})
	mcpSvc.SetEndpointPolicy(endpointPolicy)
	mcpFiles := mcp.NewFileService(files, resources, mcpSvc)
	env.rebuild(t, func(d *server.Deps) {
		d.PluginFiles = files
		d.MCP = mcpSvc
		d.MCPFiles = mcpFiles
	})
	return &fileMCPTestEnv{testEnv: env, resources: resources, mcpSvc: mcpSvc}
}

func fileMCPServerID(key pluginpkg.ResourceKey, serverKey string) string {
	payload, err := json.Marshal(struct {
		ResourceID string `json:"resource_id"`
		ServerKey  string `json:"server_key"`
	}{key.ID(), serverKey})
	if err != nil {
		return ""
	}
	return "mcp-file:" + base64.RawURLEncoding.EncodeToString(payload)
}

type pluginOAuthFixture struct {
	childID string
	digest  string
}

func installPluginOAuthFixture(t *testing.T, env *fileMCPTestEnv, scope, userID, agentID, mode, endpoint string) pluginOAuthFixture {
	t.Helper()
	name := "oauth-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	key := pluginpkg.ResourceKey{Scope: pluginpkg.Scope(scope), UserID: userID, AgentID: agentID, Kind: pluginpkg.ResourceMCP, Name: name}
	data, err := json.Marshal(mcpconfig.Declaration{URL: endpoint, Transport: mcp.TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: mcp.AuthTypeOAuth, Mode: mode}})
	if err != nil {
		t.Fatalf("marshal MCP declaration: %v", err)
	}
	resource, err := env.resources.WriteMCP(context.Background(), key, "", data)
	if err != nil {
		t.Fatalf("seed file MCP declaration: %v", err)
	}
	return pluginOAuthFixture{childID: fileMCPServerID(key, name), digest: resource.Digest}
}

func setupPluginOAuthHTTPEnv(t *testing.T, endpointPolicy mcp.EndpointPolicy) *fileMCPTestEnv {
	t.Helper()
	return setupFileMCPTestEnv(t, endpointPolicy)
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
		if rr := doRequest(t, env.testEnv, http.MethodPost, wrongParent, map[string]string{"expected_digest": "unknown"}); rr.Code != http.StatusNotFound {
			t.Fatalf("wrong parent %s status = %d, want 404 (body: %s)", action, rr.Code, rr.Body.String())
		}
		unknownConfig := fmt.Sprintf("/api/mcp/servers/%s/oauth/%s", uuid.NewString(), action)
		if rr := doRequest(t, env.testEnv, http.MethodPost, unknownConfig, map[string]string{"expected_digest": "unknown"}); rr.Code != http.StatusNotFound {
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
		want    int
	}{
		{name: "user-agent", fixture: userAgent, want: http.StatusNotFound},
		{name: "system-agent", fixture: systemAgent, want: http.StatusNotFound},
		{name: "shared-system", fixture: systemShared, want: http.StatusForbidden},
	} {
		for _, action := range []string{"start", "disconnect"} {
			path := fmt.Sprintf("/api/mcp/servers/%s/oauth/%s", test.fixture.childID, action)
			body := any(nil)
			if action == "start" {
				body = map[string]string{"expected_digest": test.fixture.digest}
			}
			if rr := doRequestWithSession(t, env.srv, userToken, http.MethodPost, path, body); rr.Code != test.want {
				t.Fatalf("unauthorized %s %s status = %d, want %d (body: %s)", test.name, action, rr.Code, test.want, rr.Body.String())
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
	rr := doRequestWithSession(t, env.srv, userToken, http.MethodPost, path, map[string]string{"expected_digest": fixture.digest})
	if rr.Code != http.StatusConflict {
		t.Fatalf("per-user initialization status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "administrator must initialize this connection before users can authorize their own accounts") {
		t.Fatalf("initialization hint missing from response: %s", rr.Body.String())
	}
}
