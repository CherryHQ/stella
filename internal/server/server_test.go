package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	lcmmemory "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/pluginstate"
	reflectplugin "github.com/CherryHQ/stella/internal/reflect"
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
)

// templateDBOnce builds a fully-migrated and seeded SQLite template DB once.
// Each test copies the file instead of re-running 48 migrations from scratch.
var (
	templateDBOnce sync.Once
	templateDBPath string
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func ensureTemplateDB() string {
	templateDBOnce.Do(func() {
		dir, err := os.MkdirTemp("", "stella_srv_tmpl_*")
		if err != nil {
			panic(fmt.Sprintf("ensureTemplateDB: MkdirTemp: %v", err))
		}
		path := filepath.Join(dir, "template.db")
		db, err := appdb.OpenDB(path)
		if err != nil {
			panic(fmt.Sprintf("ensureTemplateDB: OpenDB: %v", err))
		}
		ctx := context.Background()
		store := config.NewDBStore(db)
		if err := store.SeedDefaults(ctx); err != nil {
			panic(fmt.Sprintf("ensureTemplateDB: SeedDefaults: %v", err))
		}
		as := appdb.NewAuthStore(db)
		if err := auth.SeedPolicies(ctx, as); err != nil {
			panic(fmt.Sprintf("ensureTemplateDB: SeedPolicies: %v", err))
		}
		if err := db.Close(); err != nil {
			panic(fmt.Sprintf("ensureTemplateDB: Close: %v", err))
		}
		templateDBPath = path
	})
	return templateDBPath
}

func copyTemplateDB(t *testing.T) string {
	t.Helper()
	src := ensureTemplateDB()
	dst := filepath.Join(t.TempDir(), "test.db")
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("copyTemplateDB: open src: %v", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("copyTemplateDB: create dst: %v", err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copyTemplateDB: copy: %v", err)
	}
	return dst
}

type testEnv struct {
	srv         *server.Server
	db          *sql.DB
	store       config.Store
	pluginHost  *pluginhost.Host
	authStore   *appdb.AuthStore
	oidcStore   *appdb.OIDCStore
	mem         memory.Provider
	adminUser   auth.User
	bearerToken string
}

func setupAdmin(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("STELLA_HOME", filepath.Join(t.TempDir(), "stella-home"))
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	dbPath := copyTemplateDB(t)
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := config.NewDBStore(db)
	as := appdb.NewAuthStore(db)

	engine, err := auth.NewEngine(context.Background(), as)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	mem, err := lcmmemory.New(db, nil, nil)
	if err != nil {
		t.Fatalf("Build lcm provider: %v", err)
	}
	dispatcher := notify.NewDispatcher()

	// Override channel runtime factories BEFORE LoadDefaultCatalog so the closures
	// inside plugin.Register (RuntimeFactory: newRuntime) capture the test factories,
	// not the real ones. The real factories start actual bots (Lark SDK cron goroutines,
	// Weixin notifystart HTTP calls) that make network calls and starve SQLite goroutines.
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
	if err := phost.ApplyPlugin(context.Background(), reflectplugin.PluginID); err != nil {
		t.Fatalf("ApplyPlugin(reflect): %v", err)
	}
	srv := server.New(store, as, engine, mem, db, auth.NewLinkCodeStore(), nil, phost)

	oidcStore := appdb.NewOIDCStore(db)
	tokenSvc := auth.NewTokenService(as, nil)
	srv.SetTokenService(tokenSvc)
	srv.SetUserStore(oidcStore)
	srv.SetMembershipStore(oidcStore)
	srv.SetLoginIdentityStore(oidcStore)
	srv.SetSessionStore(oidcStore)
	srv.SetCredentialStore(oidcStore)

	// Create an admin user for authenticated requests.
	adminUser, bearerToken := createTestUserWithToken(t, as, oidcStore, "testadmin", auth.RoleAdmin)

	// Seed a password credential for the admin user so change-password tests work.
	hash, err := auth.HashPassword("testpassword")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := oidcStore.CreateCredential(context.Background(), auth.Credential{
		ID:           uuid.NewString(),
		UserID:       adminUser.ID,
		PasswordHash: hash,
	}); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	return &testEnv{
		srv:         srv,
		db:          db,
		store:       store,
		pluginHost:  phost,
		authStore:   as,
		oidcStore:   oidcStore,
		mem:         mem,
		adminUser:   adminUser,
		bearerToken: bearerToken,
	}
}

// createTestUserWithToken creates a user, organization, membership, and bearer token for testing.
func createTestUserWithToken(t *testing.T, as *appdb.AuthStore, oidcStore *appdb.OIDCStore, name, role string) (auth.User, string) {
	t.Helper()
	ctx := context.Background()
	user, err := oidcStore.CreateUser(ctx, auth.User{
		ID:       uuid.NewString(),
		Email:    name + "@test.local",
		Name:     name,
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateUser %q: %v", name, err)
	}
	org, err := oidcStore.CreateOrganization(ctx, auth.Organization{
		ID:         uuid.NewString(),
		Name:       name + "-org",
		Source:     "test",
		ExternalID: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("CreateOrganization %q: %v", name, err)
	}
	_, err = oidcStore.CreateMembership(ctx, auth.Membership{
		ID:             uuid.NewString(),
		UserID:         user.ID,
		OrganizationID: org.ID,
		Role:           role,
		IsActive:       true,
	})
	if err != nil {
		t.Fatalf("CreateMembership %q: %v", name, err)
	}
	rawToken := "stella_test_" + uuid.NewString()
	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])
	prefix := rawToken
	if len(prefix) > 15 {
		prefix = prefix[:15]
	}
	_, err = as.CreateUserToken(ctx, auth.UserToken{
		ID:          uuid.NewString(),
		UserID:      user.ID,
		Name:        "test",
		TokenHash:   tokenHash,
		TokenPrefix: prefix,
	})
	if err != nil {
		t.Fatalf("CreateUserToken %q: %v", name, err)
	}
	return user, rawToken
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
	return doRequestWithSession(t, env.srv, env.bearerToken, method, path, body)
}

func doRequestWithSession(t *testing.T, srv *server.Server, bearerToken, method, path string, body any) *httptest.ResponseRecorder {
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
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
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

	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "regularuser", auth.RoleUser)

	rr := doRequestWithSession(t, env.srv, userToken, "GET", "/channels", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("channels page status = %d, want %d", rr.Code, http.StatusOK)
	}

	rr = doRequestWithSession(t, env.srv, userToken, "GET", "/api/channels/public", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("public channels status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, userToken, "GET", "/api/channels", nil)
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
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "regularuser", auth.RoleUser)

	// Admin-only API should return 403.
	rr := doRequestWithSession(t, env.srv, userToken, "GET", "/api/providers", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	// All page routes serve the SPA shell regardless of admin status;
	// access control is enforced client-side and via the API.
	rr = doRequestWithSession(t, env.srv, userToken, "GET", "/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

// --- Skills tests ---

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
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "regularuser-search", auth.RoleUser)
	rr = doRequestWithSession(t, env.srv, userToken, "GET", "/api/skills/search", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("user missing q: status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	// Unauthenticated → 401.
	rr = doUnauthRequest(t, env.srv, "GET", "/api/skills/search?q=react", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth: status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}
