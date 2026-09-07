package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

func TestWritePluginErrorMapsOAuthClientInitialization(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePluginError(recorder, mcp.ErrOAuthClientInitializationRequired)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("OAuth client initialization status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != http.StatusConflict {
		t.Fatalf("error code = %d, want %d", body.Error.Code, http.StatusConflict)
	}
	const want = "administrator must initialize this connection before users can authorize their own accounts"
	if body.Error.Message != want {
		t.Fatalf("error message = %q, want %q", body.Error.Message, want)
	}
}

func TestMCPServerViewDoesNotEchoEndpoint(t *testing.T) {
	reg := mcp.Registration{
		ID: "0190b2c2-6f8e-7c62-9f7e-ff9f7d0c7a11", PluginID: "custom/github",
		Scope: mcp.ScopeUser, UserID: "0190b2c2-6f8e-7c62-9f9f-7d0c7a110001",
		Name: "GitHub", URL: "https://user:secret@private.example/path?token=secret",
		Transport: mcp.TransportStreamableHTTP, AuthType: mcp.AuthTypeOAuth,
		CredentialMode: mcp.CredentialModePerUser, OAuthClientID: "public-client",
		OAuthClientSecretRef: "MCP_OAUTH_CLIENT_PRIVATE", ConfigRevision: 4,
		CreatedAt: time.Date(2026, 9, 6, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
		UpdatedAt: time.Date(2026, 9, 6, 0, 0, 1, 0, time.FixedZone("CST", 8*60*60)),
	}
	view := mcpServerView(reg)
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	for _, forbidden := range []string{"private.example", "user:secret", "public-client", "MCP_OAUTH_CLIENT_PRIVATE"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("OAuth response exposed %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"oauth_client_id_configured":true`) {
		t.Fatalf("OAuth summary omitted client state: %s", encoded)
	}
}

func TestPluginAccessAuthenticationPrecedesUnavailableService(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	unauthenticated := httptest.NewRecorder()
	server.ListPlugins(unauthenticated, request, apiserver.ListPluginsParams{})
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	authenticatedRequest := request.WithContext(withAuthInfo(request.Context(), &AuthInfo{
		UserID: "user-1", Role: auth.RoleUser,
	}))
	serviceUnavailable := httptest.NewRecorder()
	server.ListPlugins(serviceUnavailable, authenticatedRequest, apiserver.ListPluginsParams{})
	if serviceUnavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable service status = %d, want %d", serviceUnavailable.Code, http.StatusServiceUnavailable)
	}
}

func TestCompactMCPInputDoesNotClassifyParentResourceOverlays(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"empty CLI overlay": {"binaries": []any{}},
		"MCP envelope": {"mcp_servers": map[string]any{"main": map[string]any{
			"url": "https://mcp.example.test", "transport": "streamable_http", "auth_type": "none",
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			if hasCompactMCPInput(&payload, nil) {
				t.Fatal("parent resource overlay selected compact MCP adapter")
			}
		})
	}
	compact := map[string]any{"auth_type": "none"}
	if !hasCompactMCPInput(&compact, nil) {
		t.Fatal("compact MCP auth field did not select MCP adapter")
	}
	if !hasCompactMCPInput(nil, &map[string]any{"token": "secret"}) {
		t.Fatal("MCP credentials did not select MCP adapter")
	}
}

func TestPluginDefinitionViewProjectsOnlySafeSummary(t *testing.T) {
	definition := pluginpkg.Definition{
		ID: "custom/plugin", DisplayName: "Plugin",
		Source: pluginpkg.SourceCustom, Revision: 1,
		Spec: json.RawMessage(`{"description":"safe","category":"utility","origin":"remote_mcp","capabilities":["read"],"url":"https://private.example/path?token=secret","credential_refs":{"token":"vault://secret"}}`),
	}
	view, err := pluginDefinitionView(definition)
	if err != nil {
		t.Fatalf("pluginDefinitionView: %v", err)
	}
	if view.Spec["description"] != "safe" || view.Spec["category"] != "utility" {
		t.Fatalf("safe summary = %#v", view.Spec)
	}
	if view.Spec["origin"] != "remote_mcp" {
		t.Fatalf("safe origin = %#v, want remote_mcp", view.Spec["origin"])
	}
	if _, ok := view.Spec["url"]; ok {
		t.Fatal("definition view exposed private url")
	}
	if _, ok := view.Spec["credential_refs"]; ok {
		t.Fatal("definition view exposed credential refs")
	}
}

func TestPluginConfigViewProjectsTypedSummary(t *testing.T) {
	enabled := true
	config := pluginpkg.Config{
		ID: "0190b2c2-6f8e-7c62-9f7e-ff9f7d0c7a11", PluginID: "custom/plugin",
		Scope: pluginpkg.ScopeUser, Enabled: &enabled,
		Payload:        json.RawMessage(`{"mcp_servers":{"main":{"url":"https://user:secret@private.example/path?token=secret#fragment","transport":"sse","auth_type":"bearer"}}}`),
		CredentialRefs: json.RawMessage(`{"mcp_servers":{"main":{"bearer":{"name":"vault-secret"}}}}`), Revision: 1,
	}
	definition := pluginpkg.Definition{ID: "custom/plugin", Spec: json.RawMessage(`{"mcp_servers":{"main":{}}}`)}
	view, err := pluginConfigView(definition, config)
	if err != nil {
		t.Fatalf("pluginConfigView: %v", err)
	}
	if len(view.ResourceSummary.McpServers) != 1 {
		t.Fatalf("MCP summaries = %#v, want one", view.ResourceSummary.McpServers)
	}
	mcpSummary := view.ResourceSummary.McpServers[0]
	if !mcpSummary.EndpointConfigured || !mcpSummary.BearerConfigured {
		t.Fatalf("MCP summary flags = %#v, want endpoint and bearer configured", mcpSummary)
	}
	encoded, err := json.Marshal(view.ResourceSummary)
	if err != nil {
		t.Fatalf("marshal backend summary: %v", err)
	}
	for _, forbidden := range []string{"private.example", "vault://", "user:secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("backend summary exposed private payload %q: %s", forbidden, encoded)
		}
	}
}

func TestPluginMCPBackendSummaryProjectsOnlyConfigurationFlags(t *testing.T) {
	definition := json.RawMessage(`{
		"mcp_servers":{"main":{
			"url":"https://user:definition-secret@private.example/definition/path?token=definition-secret#fragment",
			"transport":"streamable_http",
			"auth_type":"oauth",
			"credential_mode":"per_user",
			"metadata":{"oauth":{"client_id":"public-client-id"},"private":"metadata-secret"}
		}}
	}`)
	config := json.RawMessage(`{
		"mcp_servers":{"main":{
			"url":"https://user:config-secret@private.example/config/path?token=config-secret#fragment",
			"metadata":{"oauth":{"client_id":"config-client-id"}}
		}}
	}`)
	refs := json.RawMessage(`{
		"mcp_servers": {
			"main": {
				"bearer":"vault://bearer",
				"oauth_bundle":"vault://config-bundle",
				"oauth_client_secret":"vault://config-secret"
			}
		}
	}`)

	summaries, err := mcpResourceSummaries(definition, config, refs, nil, 1)
	if err != nil {
		t.Fatalf("mcpResourceSummaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("MCP summaries = %#v, want one", summaries)
	}
	summary := summaries[0]
	if !summary.EndpointConfigured || !summary.BearerConfigured || !summary.OauthClientIdConfigured || !summary.OauthClientSecretConfigured {
		t.Fatalf("MCP summary flags = %#v, want all configured flags true", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal MCP summary: %v", err)
	}
	for _, forbidden := range []string{
		"private.example", "definition-secret", "config-secret", "vault://", "public-client-id", "config-client-id", "metadata",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("MCP backend summary exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestPluginMCPBackendSummaryProjectsCredentialFlagsPerChild(t *testing.T) {
	definition := json.RawMessage(`{
		"mcp_servers": {
			"alpha": {"url":"https://alpha.example.test","transport":"streamable_http","auth_type":"bearer"},
			"beta": {"url":"https://beta.example.test","transport":"sse","auth_type":"oauth","metadata":{"oauth":{"client_id":"beta-client"}}}
		}
	}`)
	refs := json.RawMessage(`{
		"mcp_servers": {
			"alpha": {"bearer":{"name":"alpha-bearer"}},
			"beta": {"oauth_client_secret":{"name":"beta-secret"}}
		}
	}`)

	summaries, err := mcpResourceSummaries(definition, nil, refs, nil, 1)
	if err != nil {
		t.Fatalf("mcpResourceSummaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("MCP summaries = %#v, want two", summaries)
	}
	alpha, beta := summaries[0], summaries[1]
	if alpha.ServerKey != "alpha" || !alpha.EndpointConfigured || !alpha.BearerConfigured || alpha.OauthClientIdConfigured || alpha.OauthClientSecretConfigured {
		t.Fatalf("alpha summary flags = %#v", alpha)
	}
	if beta.ServerKey != "beta" || !beta.EndpointConfigured || beta.BearerConfigured || !beta.OauthClientIdConfigured || !beta.OauthClientSecretConfigured {
		t.Fatalf("beta summary flags = %#v", beta)
	}
}

func TestPluginMCPBackendSummaryOmitsEmptyFormalSelection(t *testing.T) {
	definition := json.RawMessage(`{
		"origin":"remote_mcp",
		"mcp_servers":{"main":{"url":"https://mcp.example.test","transport":"streamable_http","auth_type":"none"}}
	}`)
	config := json.RawMessage(`{"mcp_servers":{}}`)
	summaries, err := mcpResourceSummaries(definition, config, nil, nil, 1)
	if err != nil {
		t.Fatalf("mcpResourceSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("MCP summaries = %#v, want empty after removing the final child", summaries)
	}
}

func TestPluginResourceSummaryOmitsInheritedResourcesForDisabledEmptyConfig(t *testing.T) {
	definition := pluginpkg.Definition{Spec: json.RawMessage(`{
		"binaries":[{"name":"tool","tool":"uv","version":"1.0"}],
		"mcp_servers":{"main":{"url":"https://mcp.example.test","auth_type":"none"}}
	}`)}
	disabled := false
	for _, payload := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		config := pluginpkg.Config{Enabled: &disabled, Payload: payload}
		summary, err := pluginResourceSummary(definition, config)
		if err != nil {
			t.Fatalf("pluginResourceSummary(%s): %v", payload, err)
		}
		if len(summary.Binaries) != 0 || len(summary.McpServers) != 0 {
			t.Fatalf("disabled empty config summary = %#v, want no inherited resources", summary)
		}
	}
}

func TestPluginCLIBackendSummaryOmitsResourceSecrets(t *testing.T) {
	definition := pluginpkg.Definition{Spec: json.RawMessage(`{"prompt":"static secret","binaries":[{"name":"tool","tool":"private/repo","version":"1.2.3","options":{"token":"secret"}}],"skills":[{"name":"skill"}],"session_env":[{"env_var":"TOKEN","source":"static","value":"secret","required":true}],"oauth":[{"provider":"github","bindings":[{"credential":"access_token","env_var":"TOKEN"}]}]}`)}
	config := pluginpkg.Config{Payload: json.RawMessage(`{}`)}
	summary, err := cliBackendSummary(definition.Spec, config.Payload, config.Enabled)
	if err != nil {
		t.Fatalf("pluginBackendSummary: %v", err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal backend summary: %v", err)
	}
	for _, forbidden := range []string{"private/repo", "token", "secret", "prompt", "github"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("CLI backend summary exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestPluginCLIBackendSummaryUsesDefinitionForDefaultConfig(t *testing.T) {
	definition := pluginpkg.Definition{
		Spec: json.RawMessage(`{"binaries":[{"name":"tool","tool":"uv","version":"1.0"}],"skills":[{"name":"docs"}],"session_env":[{"env_var":"STELLA_TOKEN","source":"oauth.token","required":true}]}`),
	}
	summary, err := cliBackendSummary(definition.Spec, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	cli := summary
	if len(cli.Binaries) != 1 || cli.Binaries[0].Version != "1.0" || len(cli.Skills) != 1 || len(cli.SessionEnv) != 1 {
		t.Fatalf("default summary = %#v, want shipped resources", cli)
	}
}

func TestPluginCLISummaryProjectsOAuthBindingsAsRequiredEnv(t *testing.T) {
	summary, err := cliBackendSummary(json.RawMessage(`{
		"oauth": [{"provider":"github","bindings":[
			{"credential":"access_token","env_var":"GH_TOKEN"},
			{"credential":"refresh_token","env_var":"GH_REFRESH"}
		]}],
		"session_env":[{"env_var":"GH_TOKEN","source":"legacy.oauth","required":false}]
	}`), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.OauthProviderConfigured {
		t.Fatal("OAuth requirement did not mark provider as configured")
	}
	byVar := make(map[string]apitypes.PluginCLIBackendSessionEnvSummary, len(summary.SessionEnv))
	for _, env := range summary.SessionEnv {
		byVar[env.EnvVar] = env
	}
	if got := byVar["GH_TOKEN"]; got.Source != "legacy.oauth" || got.Required {
		t.Fatalf("existing session env was overwritten: %#v", got)
	}
	if got := byVar["GH_REFRESH"]; got.Source != "oauth.refresh_token" || !got.Required {
		t.Fatalf("OAuth binding projection = %#v", got)
	}
}

func TestPluginCLIBackendSummaryHonorsScopeOverlayAndNegativeConfig(t *testing.T) {
	definition := pluginpkg.Definition{
		Spec: json.RawMessage(`{"binaries":[{"name":"tool","tool":"uv","version":"1.0"},{"name":"other","tool":"bun","version":"1.0"}],"skills":[{"name":"docs"}]}`),
	}
	enabled := true
	summary, err := cliBackendSummary(definition.Spec, json.RawMessage(`{"binaries":{"tool":{"version":"2.0"}}}`), &enabled)
	if err != nil {
		t.Fatal(err)
	}
	cli := summary
	if len(cli.Binaries) != 2 || cli.Binaries[0].Name != "tool" || cli.Binaries[0].Version != "2.0" || cli.Binaries[1].Name != "other" {
		t.Fatalf("overlay summary = %#v, want declared resources with tool override", cli)
	}

	disabled := false
	summary, err = cliBackendSummary(definition.Spec, nil, &disabled)
	if err != nil {
		t.Fatal(err)
	}
	cli = summary
	if len(cli.Binaries) != 0 || len(cli.Skills) != 0 {
		t.Fatalf("negative summary = %#v, want no selected resources", cli)
	}
}

func TestPluginResourceSummaryRejectsMalformedPayload(t *testing.T) {
	definition := pluginpkg.Definition{ID: "channel/telegram/bot", Spec: json.RawMessage(`{"binaries":`)}
	if _, err := pluginResourceSummary(definition, pluginpkg.Config{}); err == nil {
		t.Fatal("pluginResourceSummary accepted malformed payload")
	}
}

func TestRawContainsAnyKeyFindsNestedCredentialFields(t *testing.T) {
	data := []byte(`{"initial_config":{"config":{"credential_refs":{"token":"vault://secret"}}}}`)
	if !rawContainsAnyKey(data, "credentials", "credential_refs") {
		t.Fatal("nested credential field was accepted")
	}
	if rawContainsAnyKey([]byte(`{"config":{"endpoint":"https://example.test"}}`), "credentials", "credential_refs") {
		t.Fatal("safe config field was rejected")
	}
}

func TestWritePluginErrorMapsUnifiedCRUDErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "scope", err: pluginpkg.ErrUnknownScope, want: http.StatusBadRequest},
		{name: "cas", err: pluginpkg.ErrConflict, want: http.StatusConflict},
		{name: "retired definition", err: pluginpkg.ErrRetiredDefinition, want: http.StatusConflict},
		{name: "builtin", err: pluginpkg.ErrBuiltinConfig, want: http.StatusConflict},
		{name: "private definition", err: authz.ErrNotFound, want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writePluginError(recorder, tc.err)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
			if tc.name == "retired definition" && !strings.Contains(recorder.Body.String(), "definition is retired") {
				t.Fatalf("retired definition response = %s", recorder.Body.String())
			}
		})
	}
}
