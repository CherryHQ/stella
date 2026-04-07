package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	annamcp "github.com/vaayne/anna/internal/mcp"
	"github.com/vaayne/anna/internal/pluginhost"
	internalreflect "github.com/vaayne/anna/internal/reflect"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pluginmemory "github.com/vaayne/anna/plugins/memory"
	_ "github.com/vaayne/anna/plugins/memory/lcm"
	mcp "github.com/vaayne/anna/plugins/tools/mcp"
)

type testEnv struct {
	srv        *admin.Server
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

	mem, err := pluginmemory.Build(context.Background(), "lcm", pluginmemory.BuildContext{DB: db})
	if err != nil {
		t.Fatalf("Build lcm provider: %v", err)
	}
	dispatcher := channel.NewDispatcher()
	phost := pluginhost.New(store)
	phost.RegisterLegacyID("mcp", annamcp.PluginID())
	if err := phost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	if err := phost.ApplyPlugin(context.Background(), "mcp"); err != nil {
		t.Fatalf("ApplyPlugin(mcp): %v", err)
	}
	phost.RegisterReflect(pluginhost.ReflectDeps{
		Parent:    context.Background(),
		DB:        db,
		Memory:    mem,
		Store:     store,
		Notifier:  dispatcher,
		Workspace: t.TempDir(),
	})
	phost.RegisterTelegram(pluginhost.TelegramDeps{
		Parent:   context.Background(),
		Handler:  testChannelHandler{},
		Notifier: dispatcher,
		NewChannel: func(cfg channel.TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return newTestChannel(channel.PlatformTelegram), nil
		},
	})
	phost.RegisterQQ(pluginhost.QQDeps{
		Parent:   context.Background(),
		Handler:  testChannelHandler{},
		Notifier: dispatcher,
		NewChannel: func(cfg channel.QQConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return newTestChannel(channel.PlatformQQ), nil
		},
	})
	phost.RegisterFeishu(pluginhost.FeishuDeps{
		Parent:   context.Background(),
		Handler:  testChannelHandler{},
		Notifier: dispatcher,
		NewChannel: func(cfg channel.FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return newTestChannel(channel.PlatformFeishu), nil
		},
	})
	phost.RegisterWeixin(pluginhost.WeixinDeps{
		Parent:   context.Background(),
		Handler:  testChannelHandler{},
		Notifier: dispatcher,
		NewChannel: func(cfg channel.WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return newTestChannel(channel.PlatformWeixin), nil
		},
	})
	if err := phost.ApplyPlugin(context.Background(), internalreflect.PluginID); err != nil {
		t.Fatalf("ApplyPlugin(reflect): %v", err)
	}
	srv := admin.New(store, as, engine, mem, db, auth.NewLinkCodeStore(), nil, phost)

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
		store:      store,
		pluginHost: phost,
		authStore:  as,
		adminUser:  user,
		sessionID:  sessionID,
	}
}

func doRequest(t *testing.T, env *testEnv, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, env.srv, env.sessionID, method, path, body)
}

func doRequestWithSession(t *testing.T, srv *admin.Server, sessionID, method, path string, body any) *httptest.ResponseRecorder {
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

func doUnauthRequest(t *testing.T, srv *admin.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, srv, "", method, path, body)
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
		"id":      "openai",
		"name":    "OpenAI",
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
		if p.ID == "openai" {
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
	if agents[0].ID != "anna" {
		t.Errorf("agent ID = %q, want %q", agents[0].ID, "anna")
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
					"transport": annamcp.TransportStdio,
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
	if plugin.ID != config.PluginID(config.PluginKindTool, annamcp.PluginName) {
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
				map[string]any{"name": "bad", "transport": annamcp.TransportStdio},
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
	if err := env.store.SetPluginEnabled(context.Background(), annamcp.PluginID(), true); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	if err := env.store.SetPluginConfig(context.Background(), annamcp.PluginID(), map[string]any{"servers": []any{}}); err != nil {
		t.Fatalf("SetPluginConfig: %v", err)
	}
	if err := env.pluginHost.ApplyPlugin(context.Background(), "mcp"); err != nil {
		t.Fatalf("ApplyPlugin: %v", err)
	}
	if _, ok := mcp.LookupRuntime(env.pluginHost.Services()); !ok {
		t.Fatal("expected mcp runtime")
	}

	rr := doRequest(t, env, "GET", "/api/plugin-status/tool/mcp", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var payload struct {
		Servers []annamcp.ServerStatus `json:"servers"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("unmarshal status payload: %v", err)
	}
	if len(payload.Servers) != 0 {
		t.Fatalf("len(payload.Servers) = %d, want 0", len(payload.Servers))
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

	rr = doRequest(t, env, "PATCH", "/api/plugins/reflect", map[string]any{"enabled": true})
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

	rr = doRequest(t, env, "PATCH", "/api/plugins/reflect", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	var plugin config.Plugin
	if err := json.Unmarshal(resp.Data, &plugin); err != nil {
		t.Fatalf("unmarshal plugin: %v", err)
	}
	if plugin.ID != internalreflect.PluginID || plugin.Enabled {
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
	if payload.Metadata["notify_enabled"] != true {
		t.Fatalf("notify_enabled = %#v, want true", payload.Metadata["notify_enabled"])
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
	if payload.Metadata["notify_enabled"] != true {
		t.Fatalf("notify_enabled = %#v, want true", payload.Metadata["notify_enabled"])
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
	if payload.Metadata["notify_enabled"] != true {
		t.Fatalf("notify_enabled = %#v, want true", payload.Metadata["notify_enabled"])
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
	if payload.Metadata["notify_enabled"] != true {
		t.Fatalf("notify_enabled = %#v, want true", payload.Metadata["notify_enabled"])
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
			if !strings.Contains(body, "Anna Admin") {
				t.Error("body missing page title")
			}
		})
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/nonexistent", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
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

	// Admin-only page should redirect to /agents.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/providers", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/agents" {
		t.Errorf("Location = %q, want %q", loc, "/agents")
	}

	// Non-admin page should be accessible.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}
