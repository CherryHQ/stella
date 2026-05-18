package server_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/pluginstate"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/internal/skills"
	mcp "github.com/CherryHQ/stella/internal/tools/mcp"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	feishuplugin "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/qq"
	telegramplugin "github.com/CherryHQ/stella/plugins/channels/telegram"
	weixinplugin "github.com/CherryHQ/stella/plugins/channels/weixin"
	lcmmemory "github.com/CherryHQ/stella/plugins/memory/lcm"
	reflectplugin "github.com/CherryHQ/stella/plugins/reflect"
)

type testEnv struct {
	srv        *server.Server
	db         *sql.DB
	store      config.Store
	pluginHost *pluginhost.Host
	authStore  auth.AuthStore
	adminUser  auth.AuthUser
	sessionID  string
}

func setupAdmin(t *testing.T) *testEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	as := appdb.NewAuthStore(db)
	if err := auth.SeedPolicies(context.Background(), as); err != nil {
		t.Fatalf("SeedPolicies: %v", err)
	}

	engine, err := auth.NewEngine(context.Background(), as)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	mem, err := lcmmemory.New(db, nil, nil)
	if err != nil {
		t.Fatalf("Build lcm provider: %v", err)
	}
	dispatcher := notify.NewDispatcher()
	stateStore := pluginstate.New(db)
	channelRuntimeServices := pluginhost.NewChannelRuntimeServices()
	channelRuntimeServices.Set(context.Background(), testChannelHandler{}, dispatcher)
	reflectRuntimeServices := pluginhost.NewReflectRuntimeServices()
	reflectRuntimeServices.Set(context.Background(), mem, store, t.TempDir(), func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		return func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
			return nil, nil
		}, nil
	})
	phost := pluginhost.New(store,
		pluginhost.WithAuthService(pluginhost.NewAuthService(as)),
		pluginhost.WithNotificationService(dispatcher),
		pluginhost.WithStateStore(stateStore),
		pluginhost.WithChannelRuntimeServices(channelRuntimeServices),
		pluginhost.WithReflectRuntimeServices(reflectRuntimeServices),
	)
	phost.RegisterBuiltinTools(mcp.NewManager())
	if err := phost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	phost.SetSkillStore(skills.New(db))
	if err := phost.ApplyPlugin(context.Background(), mcp.PluginID); err != nil {
		t.Fatalf("ApplyPlugin(mcp): %v", err)
	}
	t.Cleanup(auth.SetBcryptCostForTesting(bcrypt.MinCost))
	runtimeCtx, cancelRuntimes := context.WithCancel(context.Background())
	t.Cleanup(cancelRuntimes)
	resetTelegramRuntime := telegramplugin.SetRuntimeFactoryForTesting(func(pkgplugins.Platform) (pkgplugins.Runtime, error) {
		return telegramplugin.NewManagedRuntime(telegramplugin.RuntimeDeps{
			Parent:        runtimeCtx,
			Handler:       testChannelHandler{},
			Notifications: dispatcher,
			NewChannel: func(cfg pkgchannel.TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
				return newTestChannel(pkgchannel.PlatformTelegram), nil
			},
		}), nil
	})
	t.Cleanup(resetTelegramRuntime)
	resetFeishuRuntime := feishuplugin.SetRuntimeFactoryForTesting(func(pkgplugins.Platform) (pkgplugins.Runtime, error) {
		return feishuplugin.NewFeishuManagedRuntime(feishuplugin.FeishuRuntimeDeps{
			Parent:        runtimeCtx,
			Handler:       testChannelHandler{},
			Notifications: dispatcher,
			NewChannel: func(cfg pkgchannel.FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
				return newTestChannel(pkgchannel.PlatformFeishu), nil
			},
		}), nil
	})
	t.Cleanup(resetFeishuRuntime)
	resetWeixinRuntime := weixinplugin.SetRuntimeFactoryForTesting(func(pkgplugins.Platform) (pkgplugins.Runtime, error) {
		return weixinplugin.NewWeixinManagedRuntime(weixinplugin.WeixinRuntimeDeps{
			Parent:        runtimeCtx,
			Handler:       testChannelHandler{},
			Notifications: dispatcher,
			NewChannel: func(cfg pkgchannel.WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
				return newTestChannel(pkgchannel.PlatformWeixin), nil
			},
		}), nil
	})
	t.Cleanup(resetWeixinRuntime)
	if err := phost.ApplyPlugin(context.Background(), reflectplugin.PluginID); err != nil {
		t.Fatalf("ApplyPlugin(reflect): %v", err)
	}
	srv := server.New(store, as, engine, mem, db, auth.NewLinkCodeStore(), nil, phost)

	// Create an admin user for authenticated requests.
	hash, _ := auth.HashPassword("testpassword")
	user, err := as.CreateUser(context.Background(), "testadmin", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = as.UpdateUserRole(context.Background(), user.ID, auth.RoleAdmin)

	sessionID := auth.NewSessionID()
	_, err = as.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return &testEnv{
		srv:        srv,
		db:         db,
		store:      store,
		pluginHost: phost,
		authStore:  as,
		adminUser:  user,
		sessionID:  sessionID,
	}
}

func TestNewPanicsWithoutPluginHost(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()

	_ = server.New(nil, nil, nil, nil, nil, nil, nil, nil)
}

func doRequest(t *testing.T, env *testEnv, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, env.srv, env.sessionID, method, path, body)
}

func doRequestWithSession(t *testing.T, srv *server.Server, sessionID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func doUnauthRequest(t *testing.T, srv *server.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, srv, "", method, path, body)
}

func doBearerRequest(t *testing.T, srv *server.Server, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doBearerRequestWithSession(t, srv, "", token, method, path, body)
}

func doBearerRequestWithSession(t *testing.T, srv *server.Server, sessionID, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

type apiResponse struct {
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

func parseResponse(t *testing.T, rr *httptest.ResponseRecorder) apiResponse {
	t.Helper()
	var resp apiResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rr.Body.String())
	}
	return resp
}

type testChannel struct {
	name string
}

func newTestChannel(name string) *testChannel { return &testChannel{name: name} }

func (c *testChannel) Name() string { return c.name }
func (c *testChannel) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (c *testChannel) Stop()                                                       {}
func (c *testChannel) Notify(ctx context.Context, n pkgchannel.Notification) error { return nil }

type testChannelHandler struct{}

func (testChannelHandler) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (testChannelHandler) ListModels() []pkgchannel.ModelOption     { return nil }
func (testChannelHandler) SwitchModel(provider, model string) error { return nil }
func (testChannelHandler) ListAgents(ctx context.Context, msg pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}

func (testChannelHandler) SwitchAgent(ctx context.Context, msg pkgchannel.IncomingMessage, agentSlug string) error {
	return nil
}

func TestListProviders(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var providers []config.Provider
	if err := json.Unmarshal(resp.Data, &providers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected at least one provider")
	}
	if providers[0].ID != "anthropic" {
		t.Errorf("provider ID = %q, want %q", providers[0].ID, "anthropic")
	}
}

func TestCreateProvider(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{
		"id":      "openai-main",
		"type":    "openai",
		"name":    "OpenAI Main",
		"enabled": true,
		"api_key": "sk-test",
	}
	rr := doRequest(t, env, "POST", "/api/providers", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify it appears in list.
	rr = doRequest(t, env, "GET", "/api/providers", nil)
	resp := parseResponse(t, rr)
	var providers []config.Provider
	_ = json.Unmarshal(resp.Data, &providers)
	found := false
	for _, p := range providers {
		if p.ID == "openai-main" {
			found = true
		}
	}
	if !found {
		t.Error("created provider not found in list")
	}
}

func TestListAgents(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var agents []config.Agent
	if err := json.Unmarshal(resp.Data, &agents); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected at least one agent")
	}
	if agents[0].ID != "stella" {
		t.Errorf("agent ID = %q, want %q", agents[0].ID, "stella")
	}
}

func TestUpdateMCPPluginConfig(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{
		"config": map[string]any{
			"servers": []any{
				map[string]any{
					"name":      "github",
					"enabled":   true,
					"transport": mcp.TransportStdio,
					"command":   "npx",
				},
			},
		},
	}
	rr := doRequest(t, env, "PUT", "/api/plugin-config/tool/mcp", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var plugin config.Plugin
	if err := json.Unmarshal(resp.Data, &plugin); err != nil {
		t.Fatalf("unmarshal plugin: %v", err)
	}
	if plugin.ID != mcp.PluginID {
		t.Fatalf("plugin.ID = %q", plugin.ID)
	}
	servers, ok := plugin.Config["servers"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("unexpected plugin config: %#v", plugin.Config)
	}
}

func TestUpdateMCPPluginConfigRejectsInvalidConfig(t *testing.T) {
	env := setupAdmin(t)
	body := map[string]any{
		"config": map[string]any{
			"servers": []any{
				map[string]any{"name": "bad", "transport": mcp.TransportStdio},
			},
		},
	}
	rr := doRequest(t, env, "PUT", "/api/plugin-config/tool/mcp", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestGetMCPPluginStatus(t *testing.T) {
	env := setupAdmin(t)
	if err := env.store.SetPluginEnabled(context.Background(), mcp.PluginID, true); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	if err := env.store.SetPluginConfig(context.Background(), mcp.PluginID, map[string]any{"servers": []any{}}); err != nil {
		t.Fatalf("SetPluginConfig: %v", err)
	}
	if err := env.pluginHost.ApplyPlugin(context.Background(), mcp.PluginID); err != nil {
		t.Fatalf("ApplyPlugin: %v", err)
	}
	if _, ok := mcp.LookupRuntime(env.pluginHost.Runtime()); !ok {
		t.Fatal("expected mcp runtime")
	}

	rr := doRequest(t, env, "GET", "/api/plugin-status/tool/mcp", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var payload struct {
		Servers []mcp.ServerStatus `json:"servers"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal status payload: %v", err)
	}
	if len(payload.Servers) != 0 {
		t.Fatalf("len(payload.Servers) = %d, want 0", len(payload.Servers))
	}
}

func TestGetMCPPluginConfigSchema(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/plugin-config-schema/tool/mcp", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var schema map[string]any
	if err := json.Unmarshal(resp.Data, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	if _, ok := props["servers"]; !ok {
		t.Fatalf("expected servers property in schema: %#v", schema)
	}
}

func TestGetTelegramPluginConfigSchema(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/plugin-config-schema/channel/telegram", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var schema map[string]any
	if err := json.Unmarshal(resp.Data, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	if _, ok := props["token"]; !ok {
		t.Fatalf("expected token property in schema: %#v", schema)
	}
}

func TestGetAdditionalPluginConfigSchemas(t *testing.T) {
	env := setupAdmin(t)

	tests := []struct {
		path         string
		propertyName string
	}{
		{path: "/api/plugin-config-schema/channel/qq", propertyName: "app_id"},
		{path: "/api/plugin-config-schema/channel/feishu", propertyName: "app_id"},
		{path: "/api/plugin-config-schema/channel/weixin", propertyName: "bot_token"},
		{path: "/api/plugin-config-schema/reflect/reflect", propertyName: "interval"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rr := doRequest(t, env, "GET", tt.path, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
			}

			resp := parseResponse(t, rr)
			var schema map[string]any
			if err := json.Unmarshal(resp.Data, &schema); err != nil {
				t.Fatalf("unmarshal schema: %v", err)
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("schema properties = %#v", schema["properties"])
			}
			if _, ok := props[tt.propertyName]; !ok {
				t.Fatalf("expected %q property in schema: %#v", tt.propertyName, schema)
			}
		})
	}
}

func TestListPluginsUsesHostDiscoveryMetadataAndRedaction(t *testing.T) {
	env := setupAdmin(t)

	if err := env.store.SetPluginConfig(context.Background(), reflectplugin.PluginID, map[string]any{
		"interval": "30m",
		"batch":    3,
	}); err != nil {
		t.Fatalf("SetPluginConfig(reflect): %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/plugins", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	type pluginListItem struct {
		ID           string         `json:"id"`
		Kind         string         `json:"kind"`
		Enabled      bool           `json:"enabled"`
		Config       map[string]any `json:"config"`
		DisplayName  string         `json:"display_name"`
		Description  string         `json:"description"`
		Managed      bool           `json:"managed"`
		AdminVisible bool           `json:"admin_visible"`
		HasConfig    bool           `json:"has_config"`
		HasStatus    bool           `json:"has_status"`
		Capabilities []string       `json:"capabilities"`
	}
	var plugins []pluginListItem
	if err := json.Unmarshal(resp.Data, &plugins); err != nil {
		t.Fatalf("unmarshal plugins: %v", err)
	}

	byID := map[string]pluginListItem{}
	for _, plugin := range plugins {
		byID[plugin.ID] = plugin
	}

	telegram := byID[config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram)]
	if telegram.DisplayName != "Telegram" || !telegram.Managed || !telegram.AdminVisible || telegram.HasConfig || !telegram.HasStatus {
		t.Fatalf("unexpected telegram plugin payload: %#v", telegram)
	}
	if telegram.Description != "Telegram bot integration." {
		t.Fatalf("telegram description = %q, want %q", telegram.Description, "Telegram bot integration.")
	}
	if len(telegram.Config) != 0 {
		t.Fatalf("expected channel plugin config to be hidden, got %#v", telegram.Config)
	}

	qq := byID[config.PluginID(config.PluginKindChannel, pkgchannel.PlatformQQ)]
	if qq.DisplayName != "QQ" {
		t.Fatalf("unexpected qq plugin payload: %#v", qq)
	}

	reflect := byID[reflectplugin.PluginID]
	if reflect.DisplayName != "Reflect" || !reflect.Managed || !reflect.HasConfig || !reflect.HasStatus {
		t.Fatalf("unexpected reflect plugin payload: %#v", reflect)
	}
	if reflect.Description == "" {
		t.Fatalf("expected reflect description in payload: %#v", reflect)
	}
}

func TestChannelPluginConfigEndpointsRejected(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/plugin-config/channel/telegram", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("GET status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	rr = doRequest(t, env, "PUT", "/api/plugin-config/channel/telegram", map[string]any{
		"config": map[string]any{"token": "telegram-secret"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestReflectPluginConfigAndStatus(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/plugin-config/reflect/reflect", map[string]any{
		"config": map[string]any{"interval": "30m", "batch": 3},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("config status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "PATCH", "/api/plugins/reflect/reflect", map[string]any{"enabled": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/reflect/reflect", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var payload struct {
		State    string         `json:"state"`
		Message  string         `json:"message"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal reflect status: %v", err)
	}
	if payload.State != "running" {
		t.Fatalf("reflect state = %q, want running", payload.State)
	}
	if payload.Metadata["interval"] != "30m0s" {
		t.Fatalf("interval metadata = %#v, want %q", payload.Metadata["interval"], "30m0s")
	}
	if payload.Metadata["batch"] != float64(3) {
		t.Fatalf("batch metadata = %#v, want 3", payload.Metadata["batch"])
	}

	rr = doRequest(t, env, "PATCH", "/api/plugins/reflect/reflect", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	var plugin config.Plugin
	if err := json.Unmarshal(resp.Data, &plugin); err != nil {
		t.Fatalf("unmarshal plugin: %v", err)
	}
	if plugin.ID != reflectplugin.PluginID || plugin.Enabled {
		t.Fatalf("unexpected plugin payload: %#v", plugin)
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/reflect/reflect", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status after disable = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal reflect status after disable: %v", err)
	}
	if payload.State != "stopped" {
		t.Fatalf("reflect state after disable = %q, want stopped", payload.State)
	}
}

func TestUpdateTelegramChannelUsesPluginHostRuntime(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/channels/telegram", map[string]any{
		"enabled": true,
		"config":  `{"token":"tg-token","enable_notify":true,"group_mode":"mention"}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/telegram", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var payload struct {
		State    string         `json:"state"`
		Message  string         `json:"message"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal telegram status: %v", err)
	}
	if payload.State != "running" {
		t.Fatalf("telegram state = %q, want running", payload.State)
	}
	rr = doRequest(t, env, "PATCH", "/api/plugins/channel/telegram", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/telegram", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status after disable = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal telegram status after disable: %v", err)
	}
	if payload.State != "stopped" {
		t.Fatalf("telegram state after disable = %q, want stopped", payload.State)
	}
}

func TestUpdateQQChannelUsesPluginHostRuntime(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/channels/qq", map[string]any{
		"enabled": true,
		"config":  `{"app_id":"qq-app","app_secret":"qq-secret","enable_notify":true,"group_mode":"mention"}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/qq", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var payload struct {
		State    string         `json:"state"`
		Message  string         `json:"message"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal qq status: %v", err)
	}
	if payload.State != "running" {
		t.Fatalf("qq state = %q, want running", payload.State)
	}
	rr = doRequest(t, env, "PATCH", "/api/plugins/channel/qq", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/qq", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status after disable = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal qq status after disable: %v", err)
	}
	if payload.State != "stopped" {
		t.Fatalf("qq state after disable = %q, want stopped", payload.State)
	}
}

func TestUpdateFeishuChannelUsesPluginHostRuntime(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/channels/feishu", map[string]any{
		"enabled": true,
		"config":  `{"app_id":"fs-app","app_secret":"fs-secret","encrypt_key":"enc","verification_token":"verify","enable_notify":true,"group_mode":"mention","groups":{"oc_123":{"group_mode":"always","system_prompt":"be brief"}}}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/feishu", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var payload struct {
		State    string         `json:"state"`
		Message  string         `json:"message"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal feishu status: %v", err)
	}
	if payload.State != "running" {
		t.Fatalf("feishu state = %q, want running", payload.State)
	}
	if payload.Metadata["group_count"] != float64(1) {
		t.Fatalf("group_count = %#v, want 1", payload.Metadata["group_count"])
	}

	rr = doRequest(t, env, "PATCH", "/api/plugins/channel/feishu", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/feishu", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status after disable = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal feishu status after disable: %v", err)
	}
	if payload.State != "stopped" {
		t.Fatalf("feishu state after disable = %q, want stopped", payload.State)
	}
}

func TestUpdateWeixinChannelUsesPluginHostRuntime(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/channels/weixin", map[string]any{
		"enabled": true,
		"config":  `{"bot_token":"wx-token","base_url":"https://wx.example","bot_id":"bot-1","user_id":"user-1","enable_notify":true}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/weixin", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var payload struct {
		State    string         `json:"state"`
		Message  string         `json:"message"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal weixin status: %v", err)
	}
	if payload.State != "running" {
		t.Fatalf("weixin state = %q, want running", payload.State)
	}
	if payload.Metadata["has_bot_identity"] != true {
		t.Fatalf("has_bot_identity = %#v, want true", payload.Metadata["has_bot_identity"])
	}

	rr = doRequest(t, env, "PATCH", "/api/plugins/channel/weixin", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugin-status/channel/weixin", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status after disable = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal weixin status after disable: %v", err)
	}
	if payload.State != "stopped" {
		t.Fatalf("weixin state after disable = %q, want stopped", payload.State)
	}
}

func TestPublicChannelsOnlyIncludeEnabledChannels(t *testing.T) {
	env := setupAdmin(t)

	if err := env.store.UpsertChannel(context.Background(), config.Channel{
		ID:      pkgchannel.PlatformTelegram,
		Type:    pkgchannel.PlatformTelegram,
		Enabled: true,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel telegram: %v", err)
	}
	if err := env.store.UpsertChannel(context.Background(), config.Channel{
		ID:      pkgchannel.PlatformFeishu,
		Type:    pkgchannel.PlatformFeishu,
		Enabled: false,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel feishu: %v", err)
	}
	if err := env.store.UpsertChannel(context.Background(), config.Channel{
		ID:      "feishu-stella",
		Type:    pkgchannel.PlatformFeishu,
		AgentID: "stella",
		Enabled: true,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel feishu-stella: %v", err)
	}
	if err := env.store.UpsertPlugin(context.Background(), config.Plugin{
		ID:      config.PluginID(config.PluginKindChannel, pkgchannel.PlatformQQ),
		Kind:    config.PluginKindChannel,
		Name:    pkgchannel.PlatformQQ,
		Enabled: false,
		Config:  map[string]any{},
	}); err != nil {
		t.Fatalf("UpsertPlugin qq: %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/channels/public", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	type publicChannelPayload struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Label     string `json:"label"`
		AgentID   string `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Enabled   bool   `json:"enabled"`
	}
	var channels []publicChannelPayload
	if err := json.Unmarshal(resp.Data, &channels); err != nil {
		t.Fatalf("unmarshal public channels: %v", err)
	}
	byID := make(map[string]publicChannelPayload, len(channels))
	for _, channel := range channels {
		if !channel.Enabled {
			t.Fatalf("channel %q is disabled", channel.ID)
		}
		byID[channel.ID] = channel
	}
	if _, ok := byID[pkgchannel.PlatformTelegram]; !ok {
		t.Fatalf("expected telegram public channel, got %#v", channels)
	}
	if _, ok := byID[pkgchannel.PlatformFeishu]; ok {
		t.Fatalf("feishu disabled channel should not be public: %#v", channels)
	}
	if _, ok := byID[pkgchannel.PlatformQQ]; ok {
		t.Fatalf("qq disabled plugin should not be public: %#v", channels)
	}
	if _, ok := byID["feishu-stella"]; ok {
		t.Fatalf("dedicated instances should not appear in public channels: %#v", channels)
	}
}

func TestUpdateChannelConfigPreservesEnabledState(t *testing.T) {
	env := setupAdmin(t)

	if err := env.store.UpsertChannel(context.Background(), config.Channel{
		ID:      pkgchannel.PlatformTelegram,
		Type:    pkgchannel.PlatformTelegram,
		Enabled: false,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel telegram: %v", err)
	}
	if err := env.store.UpsertPlugin(context.Background(), config.Plugin{
		ID:      config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram),
		Kind:    config.PluginKindChannel,
		Name:    pkgchannel.PlatformTelegram,
		Enabled: true,
		Config:  map[string]any{},
	}); err != nil {
		t.Fatalf("UpsertPlugin telegram: %v", err)
	}

	rr := doRequest(t, env, "PUT", "/api/channels/telegram", map[string]any{
		"enabled": true,
		"config":  `{"token":"tg-token","enable_notify":true}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	ch, err := env.store.GetChannel(context.Background(), pkgchannel.PlatformTelegram)
	if err != nil {
		t.Fatalf("GetChannel telegram: %v", err)
	}
	if ch.Enabled {
		t.Fatal("channel config update should not enable channel")
	}
	plugin, err := env.store.GetPlugin(context.Background(), config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram))
	if err != nil {
		t.Fatalf("GetPlugin telegram: %v", err)
	}
	if !plugin.Enabled {
		t.Fatal("channel config update should not disable channel plugin")
	}
}

func TestNonAdminCanOpenChannelsPageButNotChannelConfig(t *testing.T) {
	env := setupAdmin(t)

	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(context.Background(), "regularuser", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sessionID := auth.NewSessionID()
	_, err = env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/channels", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("channels page status = %d, want %d", rr.Code, http.StatusOK)
	}

	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/channels/public", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("public channels status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/channels", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("channel config status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func TestCreateAgent(t *testing.T) {
	env := setupAdmin(t)

	body := config.Agent{
		Name:         "Coder",
		Model:        "anthropic/claude-sonnet-4-6",
		SystemPrompt: "You are a coding assistant.",
		Enabled:      true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Extract auto-generated ID from response.
	resp := parseResponse(t, rr)
	var created config.Agent
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty auto-generated ID")
	}

	// Verify via get.
	rr = doRequest(t, env, "GET", "/api/agents/"+created.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestRootRedirect(t *testing.T) {
	env := setupAdmin(t)

	// Authenticated admin -> /providers.
	rr := doRequest(t, env, "GET", "/", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/providers" {
		t.Errorf("Location = %q, want %q", loc, "/providers")
	}

	// Unauthenticated -> /login.
	rr = doUnauthRequest(t, env.srv, "GET", "/", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc = rr.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("Location = %q, want %q", loc, "/login")
	}
}

func TestPageRoutes(t *testing.T) {
	env := setupAdmin(t)

	pages := []string{
		"/providers", "/agents", "/channels",
		"/users", "/sessions", "/scheduler",
	}
	for _, path := range pages {
		t.Run(path, func(t *testing.T) {
			rr := doRequest(t, env, "GET", path, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			ct := rr.Header().Get("Content-Type")
			if ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
			}
			body := rr.Body.String()
			if len(body) == 0 {
				t.Fatal("empty body")
			}
			if !strings.Contains(body, "app-root") {
				t.Error("body missing SPA mount point")
			}
		})
	}
}

func TestUnknownPathReturnsSPA(t *testing.T) {
	env := setupAdmin(t)

	// The SPA wildcard handler serves the app shell for all unknown paths.
	rr := doRequest(t, env, "GET", "/nonexistent", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "app-root") {
		t.Error("body missing SPA mount point")
	}
}

func TestCORSPreflight(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "OPTIONS", "/api/providers", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	origin := rr.Header().Get("Access-Control-Allow-Origin")
	if origin == "" {
		t.Error("missing CORS origin header")
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("missing CORS credentials header")
	}
}

func TestLoginPageAccessible(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/login", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
}

func TestUnauthenticatedAPIReturns401(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestUnauthenticatedPageRedirectsToLogin(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/agents", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("Location = %q, want %q", loc, "/login")
	}
}

func TestNonAdminCannotAccessAdminRoutes(t *testing.T) {
	env := setupAdmin(t)

	// Create a non-admin user.
	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(context.Background(), "regularuser", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	sessionID := auth.NewSessionID()
	_, err = env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Admin-only API should return 403.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/providers", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	// All page routes serve the SPA shell regardless of admin status;
	// access control is enforced client-side and via the API.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

// --- Skills tests ---

func TestSkillsList_Admin(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Fresh DB has no skills; just verify we got a valid empty array.
	if list == nil {
		t.Fatal("expected non-nil list")
	}
}

func TestSkillsList_NonAdmin(t *testing.T) {
	env := setupAdmin(t)

	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(context.Background(), "regularuser2", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sessionID := auth.NewSessionID()
	_, err = env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/skills", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func TestSkillsCreate(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{
		"name":        "test-skill",
		"scope":       "system",
		"description": "A test skill",
		"status":      "active",
		"files": map[string]string{
			"SKILL.md": "# Test Skill\n\nDoes something useful.",
		},
	}
	rr := doRequest(t, env, "POST", "/api/skills", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created map[string]string
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if created["id"] == "" {
		t.Fatal("expected non-empty id in response")
	}

	// Verify it appears in list.
	rr = doRequest(t, env, "GET", "/api/skills", nil)
	resp = parseResponse(t, rr)
	var list []map[string]any
	_ = json.Unmarshal(resp.Data, &list)
	found := false
	for _, sk := range list {
		if sk["name"] == "test-skill" {
			found = true
		}
	}
	if !found {
		t.Error("created skill not found in list")
	}
}

func TestSkillsUpdate(t *testing.T) {
	env := setupAdmin(t)

	// Create a skill first.
	createBody := map[string]any{
		"name":  "update-skill",
		"scope": "system",
		"files": map[string]string{"SKILL.md": "# Original"},
	}
	rr := doRequest(t, env, "POST", "/api/skills", createBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created map[string]string
	_ = json.Unmarshal(resp.Data, &created)
	id := created["id"]

	// Update description, status, and SKILL.md content.
	newDesc := "Updated description"
	newStatus := "deprecated"
	updateBody := map[string]any{
		"description": newDesc,
		"status":      newStatus,
		"files":       map[string]string{"SKILL.md": "# Updated content"},
	}
	rr = doRequest(t, env, "PUT", "/api/skills/"+id, updateBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify SKILL.md content changed.
	rr = doRequest(t, env, "GET", "/api/skills/"+id+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET file status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	var file map[string]string
	_ = json.Unmarshal(resp.Data, &file)
	if file["content"] != "# Updated content" {
		t.Errorf("content = %q, want %q", file["content"], "# Updated content")
	}
}

func TestSkillsDelete(t *testing.T) {
	env := setupAdmin(t)

	createBody := map[string]any{
		"name":  "delete-skill",
		"scope": "system",
		"files": map[string]string{"SKILL.md": "# Delete me"},
	}
	rr := doRequest(t, env, "POST", "/api/skills", createBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created map[string]string
	_ = json.Unmarshal(resp.Data, &created)
	id := created["id"]

	rr = doRequest(t, env, "DELETE", "/api/skills/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Should no longer be in list.
	rr = doRequest(t, env, "GET", "/api/skills", nil)
	resp = parseResponse(t, rr)
	var list []map[string]any
	_ = json.Unmarshal(resp.Data, &list)
	for _, sk := range list {
		if sk["id"] == id {
			t.Errorf("deleted skill %q still appears in list", id)
		}
	}
}

func TestSkillsGetFile(t *testing.T) {
	env := setupAdmin(t)

	content := "# My skill\n\nDoes something."
	createBody := map[string]any{
		"name":  "file-skill",
		"scope": "system",
		"files": map[string]string{"SKILL.md": content},
	}
	rr := doRequest(t, env, "POST", "/api/skills", createBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created map[string]string
	_ = json.Unmarshal(resp.Data, &created)
	id := created["id"]

	rr = doRequest(t, env, "GET", "/api/skills/"+id+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET file status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	var file map[string]string
	if err := json.Unmarshal(resp.Data, &file); err != nil {
		t.Fatalf("unmarshal file response: %v", err)
	}
	if file["content"] != content {
		t.Errorf("content = %q, want %q", file["content"], content)
	}
	if file["path"] != "SKILL.md" {
		t.Errorf("path = %q, want %q", file["path"], "SKILL.md")
	}
}

// TestSkillsSearch_Admin verifies the search endpoint enforces auth and validates
// the q parameter. A real search against skills.sh is NOT tested here — that
// would require network access and is too fragile for unit tests. Integration /
// manual QA should cover the happy path.
func TestSkillsSearch_Authenticated(t *testing.T) {
	env := setupAdmin(t)

	// Admin with missing q → 400.
	rr := doRequest(t, env, "GET", "/api/skills/search", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("admin missing q: status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	// Authenticated non-admin with missing q → 400, proving search is no longer admin-only.
	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(context.Background(), "regularuser-search", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sessionID := auth.NewSessionID()
	if _, err = env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/skills/search", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("user missing q: status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	// Unauthenticated → 401.
	rr = doUnauthRequest(t, env.srv, "GET", "/api/skills/search?q=react", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth: status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

// TestSkillsInstall_Validation checks the install endpoint rejects bad inputs.
func TestSkillsInstall_Validation(t *testing.T) {
	env := setupAdmin(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing source", map[string]any{"scope": "system"}},
		{"empty source", map[string]any{"source": "", "scope": "system"}},
		{"scope=user no user_id", map[string]any{"source": "owner/repo@skill", "scope": "user"}},
		{"scope=agent no agent_id", map[string]any{"source": "owner/repo@skill", "scope": "agent"}},
		{"unknown scope", map[string]any{"source": "owner/repo@skill", "scope": "project"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, env, "POST", "/api/skills/install", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
			}
		})
	}
}

// TestSkillsInstall_Unauthorized checks that unauthenticated requests cannot use
// the install endpoint (401) and authenticated non-admins get 403.
func TestSkillsInstall_Unauthorized(t *testing.T) {
	env := setupAdmin(t)

	// No session → 401.
	rr := doUnauthRequest(t, env.srv, "POST", "/api/skills/install", map[string]any{
		"source": "owner/repo@skill",
		"scope":  "system",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}

	// Authenticated non-admin → 403.
	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(context.Background(), "regularuser-install", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sessionID := auth.NewSessionID()
	_, err = env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rr = doRequestWithSession(t, env.srv, sessionID, "POST", "/api/skills/install", map[string]any{
		"source": "owner/repo@skill",
		"scope":  "system",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}
