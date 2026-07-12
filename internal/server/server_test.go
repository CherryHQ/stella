package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agentaccess"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/memory"
	lcmmemory "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/pluginstate"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/internal/sessionaccess"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	feishuplugin "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/qq"
	telegramplugin "github.com/CherryHQ/stella/plugins/channels/telegram"
	weixinplugin "github.com/CherryHQ/stella/plugins/channels/weixin"
)

func TestMain(m *testing.M) {
	// Lower bcrypt cost for the whole package before handing the run+exit to
	// dbtest.Main, which stops the shared embedded Postgres server afterward.
	auth.SetBcryptCostForTesting(bcrypt.MinCost)
	dbtest.Main(m)
}

type testEnv struct {
	srv         *server.Server
	db          *pgxpool.Pool
	store       config.Store
	pluginHost  *pluginhost.Host
	authStore   *appdb.AuthStore
	oidcStore   *appdb.OIDCStore
	mem         memory.Provider
	adminUser   auth.User
	bearerToken string

	// deps is the base dependency set used to build srv. Because the server is
	// immutable (no setters), a test that needs an optional capability rebuilds
	// the server via rebuild() with a mutated copy rather than mutating srv.
	deps    server.Deps
	credSvc *connections.Service
}

// rebuild constructs a fresh server from a copy of the base deps with mutate
// applied, and swaps it into env.srv. It replaces the removed post-construction
// setters: the composition contract is that a server is built once from a
// complete Deps, so per-test configuration means a per-test build.
func (env *testEnv) rebuild(t *testing.T, mutate func(*server.Deps)) {
	t.Helper()
	d := env.deps
	mutate(&d)
	srv, err := server.New(context.Background(), d)
	if err != nil {
		t.Fatalf("rebuild server.New: %v", err)
	}
	env.srv = srv
}

func enableChannelPlugin(t *testing.T, env *testEnv, channelType string) {
	t.Helper()
	if err := env.store.SetPluginEnabled(context.Background(), config.PluginID(config.PluginKindChannel, channelType), true); err != nil {
		t.Fatalf("enable channel plugin %q: %v", channelType, err)
	}
}

func setupAdmin(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("STELLA_HOME", filepath.Join(t.TempDir(), "stella-home"))
	t.Setenv("STELLA_SCOPED_TOKEN_SECRET", "test-scoped-secret")
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	db := dbtest.New(t)

	store := cfgstore.NewDBStore(db)
	ctx := context.Background()
	_ = store.Seed(ctx)
	as := appdb.NewAuthStore(db)

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
	phost := pluginhost.New(store,
		pluginhost.WithAuthService(pluginhost.NewAuthService(as)),
		pluginhost.WithNotificationService(dispatcher),
		pluginhost.WithStateStore(stateStore),
		pluginhost.WithChannelRuntimeServices(channelRuntimeServices),
	)
	if err := phost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	skillStore := skills.New(db)
	phost.SetSkillStore(skillStore)

	oidcStore := appdb.NewOIDCStore(db)
	authSvc := auth.NewAuthService(db, oidcStore, oidcStore, oidcStore)
	sessionMgr, err := auth.NewSessionManager(oidcStore, "test-vault-key")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Build the same shared instances the composition root builds, so the test
	// server exercises the real, injected dependency set (no shadow construction).
	const baseURL = "http://localhost:25678"
	poolManager := agent.NewPoolManager(store, mem)
	recallyStore := recally.NewStore(db)
	assetHome := t.TempDir()
	assetStore, err := asset.NewStore(assetHome, nil, false, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	credFrontDoor, oauthAuthServer := server.NewCredentialFrontDoor(db, slog.With("component", "admin-test"))
	credSvc := connections.NewService(nil, sqlc.New(db), oauth.NewFlowStore(), baseURL)
	authorizer := policy.New(db)
	homeDir, _ := os.UserHomeDir()
	systemPromptBuilder, err := sessionaccess.NewSystemPromptBuilder(sessionaccess.SystemPromptDeps{
		StellaHome: config.StellaHome(),
		HomeDir:    homeDir,
		Memory:     mem,
		Agents:     sessionaccess.ConfigPromptAgentStore{Store: store},
		Projects:   sessionaccess.NewSQLPromptProjectStore(db),
		Workspace:  sessionaccess.AgentPromptWorkspace{},
		Plugins:    phost,
		SkillStore: pluginhost.NewSkillStoreAdapter(skillStore),
		Skills:     skills.BuildPromptSection,
	})
	if err != nil {
		t.Fatalf("sessionaccess.NewSystemPromptBuilder: %v", err)
	}
	sessionSvc, err := sessionaccess.NewService(mem, db, store, as, assetStore, authorizer, sessionaccess.WithSystemPromptBuilder(systemPromptBuilder))
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	deps := server.Deps{
		Store:               store,
		DB:                  db,
		AuthStore:           as,
		Mem:                 mem,
		AgentAccess:         agentaccess.NewService(store, as, authorizer),
		SessionAccess:       sessionSvc,
		SkillAccess:         skillaccess.NewService(skillStore, agentaccess.NewService(store, as, authorizer), authorizer),
		LinkCodes:           auth.NewLinkCodeStore(),
		PoolManager:         poolManager,
		PluginHost:          phost,
		BaseURL:             baseURL,
		Credentials:         credSvc,
		Email:               email.NewService(nil, sqlc.New(db), authorizer),
		Share:               sharepkg.NewService(sqlc.New(db), mem, recallyStore, assetStore, assetHome, baseURL),
		Assets:              assetStore,
		Recally:             recally.NewService(recallyStore, t.TempDir()),
		CredentialFrontDoor: credFrontDoor,
		OAuthAuthServer:     oauthAuthServer,
		OIDC: server.OIDCDeps{
			AuthSvc:     authSvc,
			SessionMgr:  sessionMgr,
			Logins:      oidcStore,
			Users:       oidcStore,
			Sessions:    oidcStore,
			Credentials: oidcStore,
		},
	}
	srv, err := server.New(ctx, deps)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

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
		deps:        deps,
		credSvc:     credSvc,
	}
}

// createTestUserWithToken creates a user and login session token for testing.
func createTestUserWithToken(t *testing.T, as *appdb.AuthStore, oidcStore *appdb.OIDCStore, name, role string) (auth.User, string) {
	t.Helper()
	ctx := context.Background()
	user, err := oidcStore.CreateUser(ctx, auth.User{
		ID:       uuid.NewString(),
		Email:    name + "@test.local",
		Name:     name,
		Role:     role,
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateUser %q: %v", name, err)
	}
	sessionMgr, err := auth.NewSessionManager(oidcStore, "test-vault-key")
	if err != nil {
		t.Fatalf("NewSessionManager %q: %v", name, err)
	}
	rawToken, _, err := sessionMgr.Create(ctx, user.ID)
	if err != nil {
		t.Fatalf("CreateSession %q: %v", name, err)
	}
	return user, rawToken
}

func TestNewErrorsWithoutRequiredDeps(t *testing.T) {
	// An empty Deps is missing every required dependency; New must fail fast with
	// an error naming them, never panic or return a half-built server.
	srv, err := server.New(context.Background(), server.Deps{})
	if err == nil {
		t.Fatal("expected error for missing required dependencies")
	}
	if srv != nil {
		t.Fatal("expected nil server on validation failure")
	}
	for _, want := range []string{"PluginHost", "Store", "BaseURL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention missing dep %q", err, want)
		}
	}
}

func doRequest(t *testing.T, env *testEnv, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, env.srv, env.bearerToken, method, path, body)
}

func doRequestWithSession(t *testing.T, srv *server.Server, sessionToken, method, path string, body any) *httptest.ResponseRecorder {
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
	if sessionToken != "" {
		if strings.HasPrefix(sessionToken, "stella_") {
			req.Header.Set("Authorization", "Bearer "+sessionToken)
		} else {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
		}
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
		if strings.HasPrefix(token, "stella_") {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		}
	}
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

type apiResponse struct {
	Data  json.RawMessage
	Error string
}

func parseResponse(t *testing.T, rr *httptest.ResponseRecorder) apiResponse {
	t.Helper()
	body := rr.Body.Bytes()
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		return apiResponse{Error: errResp.Error.Message}
	}
	return apiResponse{Data: json.RawMessage(body)}
}

// parseListItems extracts the array payload from an AIP list response. The
// response wraps results in a resource-named field (e.g. {"goals":[...]})
// alongside optional pagination metadata, so this returns the first
// array-valued field regardless of its name.
// parseListItems extracts the array stored under the explicit resource key
// (e.g. "sessions", "users") from a list response envelope. Asserting the
// known key — rather than scanning for the first array field — ensures tests
// fail when the response is shaped wrong, the failure mode behind C1.
func parseListItems(t *testing.T, rr *httptest.ResponseRecorder, key string) json.RawMessage {
	t.Helper()
	resp := parseResponse(t, rr)
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &wrapper); err != nil {
		t.Fatalf("unmarshal list wrapper: %v", err)
	}
	val, ok := wrapper[key]
	if !ok {
		t.Fatalf("list response missing %q key: %s", key, resp.Data)
	}
	trimmed := bytes.TrimSpace(val)
	if string(trimmed) == "null" {
		return json.RawMessage("[]")
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("%q is not an array in list response: %s", key, resp.Data)
	}
	return val
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

	var providers []config.Provider
	if err := json.Unmarshal(parseListItems(t, rr, "providers"), &providers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected at least one provider")
	}
	if providers[0].Type != "anthropic" {
		t.Errorf("provider Type = %q, want %q", providers[0].Type, "anthropic")
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
	var providers []config.Provider
	_ = json.Unmarshal(parseListItems(t, rr, "providers"), &providers)
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

	items := parseListItems(t, rr, "agents")
	var agents []config.Agent
	if err := json.Unmarshal(items, &agents); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected at least one agent")
	}
	if agents[0].Name != "Stella" {
		t.Errorf("agent Name = %q, want %q", agents[0].Name, "Stella")
	}
}

func TestGetTelegramPluginConfigSchema(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/plugins/channel/telegram/config-schema", nil)
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
		{path: "/api/plugins/channel/qq/config-schema", propertyName: "app_id"},
		{path: "/api/plugins/channel/feishu/config-schema", propertyName: "app_id"},
		{path: "/api/plugins/channel/weixin/config-schema", propertyName: "bot_token"},
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

	rr := doRequest(t, env, "GET", "/api/plugins", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

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
	if err := json.Unmarshal(parseListItems(t, rr, "plugins"), &plugins); err != nil {
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
}

func TestChannelPluginConfigEndpointsRejected(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/plugins/channel/telegram/config", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("GET status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	rr = doRequest(t, env, "PATCH", "/api/plugins/channel/telegram/config", map[string]any{
		"config": map[string]any{"token": "telegram-secret"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestUpdateTelegramChannelUsesPluginHostRuntime(t *testing.T) {
	env := setupAdmin(t)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)

	rr := doRequest(t, env, "PATCH", "/api/channels/telegram", map[string]any{
		"enabled": true,
		"config":  `{"token":"tg-token","enable_notify":true}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugins/channel/telegram/status", nil)
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

	rr = doRequest(t, env, "GET", "/api/plugins/channel/telegram/status", nil)
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
	enableChannelPlugin(t, env, pkgchannel.PlatformQQ)

	rr := doRequest(t, env, "PATCH", "/api/channels/qq", map[string]any{
		"enabled": true,
		"config":  `{"app_id":"qq-app","app_secret":"qq-secret","enable_notify":true}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugins/channel/qq/status", nil)
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

	rr = doRequest(t, env, "GET", "/api/plugins/channel/qq/status", nil)
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
	enableChannelPlugin(t, env, pkgchannel.PlatformFeishu)

	rr := doRequest(t, env, "PATCH", "/api/channels/feishu", map[string]any{
		"enabled": true,
		"config":  `{"app_id":"fs-app","app_secret":"fs-secret","encrypt_key":"enc","verification_token":"verify","enable_notify":true,"groups":{"oc_123":{"system_prompt":"be brief"}}}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugins/channel/feishu/status", nil)
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

	rr = doRequest(t, env, "GET", "/api/plugins/channel/feishu/status", nil)
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
	enableChannelPlugin(t, env, pkgchannel.PlatformWeixin)

	rr := doRequest(t, env, "PATCH", "/api/channels/weixin", map[string]any{
		"enabled": true,
		"config":  `{"bot_token":"wx-token","base_url":"https://wx.example","bot_id":"bot-1","user_id":"user-1","enable_notify":true}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, "GET", "/api/plugins/channel/weixin/status", nil)
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

	rr = doRequest(t, env, "GET", "/api/plugins/channel/weixin/status", nil)
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
	octx := context.Background()
	stellaID := findStellaID(t, env)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)
	enableChannelPlugin(t, env, pkgchannel.PlatformFeishu)

	if err := env.store.UpsertChannel(octx, config.Channel{
		ID:      pkgchannel.PlatformTelegram,
		Type:    pkgchannel.PlatformTelegram,
		Enabled: true,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel telegram: %v", err)
	}
	if err := env.store.UpsertChannel(octx, config.Channel{
		ID:      pkgchannel.PlatformFeishu,
		Type:    pkgchannel.PlatformFeishu,
		Enabled: false,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel feishu: %v", err)
	}
	if err := env.store.UpsertChannel(octx, config.Channel{
		ID:      "feishu-stella",
		Type:    pkgchannel.PlatformFeishu,
		AgentID: stellaID,
		Enabled: true,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel feishu-stella: %v", err)
	}
	if err := env.store.UpsertPlugin(octx, config.Plugin{
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
	type publicChannelPayload struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Label     string `json:"label"`
		AgentID   string `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Enabled   bool   `json:"enabled"`
	}
	var channels []publicChannelPayload
	if err := json.Unmarshal(parseListItems(t, rr, "channels"), &channels); err != nil {
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
		t.Fatalf("feishu disabled default should not be public: %#v", channels)
	}
	if _, ok := byID["feishu-stella"]; !ok {
		t.Fatalf("feishu-stella dedicated enabled channel should be public: %#v", channels)
	}
	if _, ok := byID[pkgchannel.PlatformQQ]; ok {
		t.Fatalf("qq disabled plugin should not be public: %#v", channels)
	}
}

func TestUpdateChannelEnabledState(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	if err := env.store.UpsertChannel(octx, config.Channel{
		ID:      pkgchannel.PlatformTelegram,
		Type:    pkgchannel.PlatformTelegram,
		Enabled: false,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel telegram: %v", err)
	}
	if err := env.store.UpsertPlugin(octx, config.Plugin{
		ID:      config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram),
		Kind:    config.PluginKindChannel,
		Name:    pkgchannel.PlatformTelegram,
		Enabled: true,
		Config:  map[string]any{},
	}); err != nil {
		t.Fatalf("UpsertPlugin telegram: %v", err)
	}

	rr := doRequest(t, env, "PATCH", "/api/channels/telegram", map[string]any{
		"enabled": true,
		"config":  `{"token":"tg-token","enable_notify":true}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	ch, err := env.store.GetChannel(octx, pkgchannel.PlatformTelegram)
	if err != nil {
		t.Fatalf("GetChannel telegram: %v", err)
	}
	if !ch.Enabled {
		t.Fatal("channel should be enabled after explicit enabled=true update")
	}

	rr = doRequest(t, env, "PATCH", "/api/channels/telegram", map[string]any{
		"config": `{"token":"tg-token-2"}`,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("config-only update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	ch, err = env.store.GetChannel(octx, pkgchannel.PlatformTelegram)
	if err != nil {
		t.Fatalf("GetChannel telegram: %v", err)
	}
	if !ch.Enabled {
		t.Fatal("config-only update should preserve enabled state")
	}

	plugin, err := env.store.GetPlugin(octx, config.PluginID(config.PluginKindChannel, pkgchannel.PlatformTelegram))
	if err != nil {
		t.Fatalf("GetPlugin telegram: %v", err)
	}
	if !plugin.Enabled {
		t.Fatal("channel plugin should remain enabled")
	}
}

// TestNonAdminCanOpenChannelsPageButNotChannelConfig removed: single-tenant mode
// grants admin to all authenticated users.

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

	checks := []struct {
		method string
		path   string
		body   any
	}{
		{"GET", "/api/agents", nil},
		{"POST", "/api/agents", map[string]any{"name": "Nope"}},
		{"GET", "/api/agents/nope", nil},
		{"GET", "/api/agents/nope/sessions/nope", nil},
	}
	for _, tc := range checks {
		rr := doUnauthRequest(t, env.srv, tc.method, tc.path, tc.body)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d, want %d (body: %s)", tc.method, tc.path, rr.Code, http.StatusUnauthorized, rr.Body.String())
		}
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

// TestNonAdminCannotAccessAdminRoutes removed: single-tenant mode grants admin
// to all authenticated users.

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

// TestSaveManifestPluginsPreservesSessionEnvVaultKey guards against the enable
// toggle clobbering the session_env_vault_key. The Save payload only carries
// the enable flag, so the handler must read the existing override row and
// preserve any session env binding instead of overwriting it with "".
func TestSaveManifestPluginsPreservesSessionEnvVaultKey(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	// tool/tap-web defaults to enabled=true in the builtin manifest (and is not
	// essential, so it can be toggled). Pre-seed an override row that binds a
	// session env vault key.
	const pluginID = "tool/tap-web"
	const vaultKey = "vault/session/tapweb"
	if err := env.store.UpsertManifestPluginOverride(octx, config.ManifestPluginOverride{
		PluginID:           pluginID,
		SessionEnvVaultKey: vaultKey,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	// Disable the plugin via the Save handler (enabled diverges from default).
	body := map[string]any{"plugins": []map[string]any{{"id": pluginID, "enabled": false}}}
	rr := doRequest(t, env, "PATCH", "/api/manifest-plugins", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("save: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	ov, ok, err := env.store.GetManifestPluginOverride(octx, pluginID)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if !ok {
		t.Fatal("override row missing after save")
	}
	if ov.SessionEnvVaultKey != vaultKey {
		t.Fatalf("session_env_vault_key clobbered: got %q, want %q", ov.SessionEnvVaultKey, vaultKey)
	}
	if ov.Enabled == nil || *ov.Enabled != false {
		t.Fatalf("enabled override not persisted: got %v, want explicit false", ov.Enabled)
	}
	if ov.Config != "" {
		t.Fatalf("toggle-only save must not store config override: got %q", ov.Config)
	}

	// Toggle back to the default (enabled=true). The row must survive because a
	// session env binding still exists; only the enable override clears to nil.
	body = map[string]any{"plugins": []map[string]any{{"id": pluginID, "enabled": true}}}
	rr = doRequest(t, env, "PATCH", "/api/manifest-plugins", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("save back: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	ov, ok, err = env.store.GetManifestPluginOverride(octx, pluginID)
	if err != nil {
		t.Fatalf("get override after reset: %v", err)
	}
	if !ok {
		t.Fatal("override row deleted despite session env binding")
	}
	if ov.SessionEnvVaultKey != vaultKey {
		t.Fatalf("session_env_vault_key lost on reset: got %q, want %q", ov.SessionEnvVaultKey, vaultKey)
	}
	if ov.Enabled != nil {
		t.Fatalf("enabled should fall back to default (nil), got %v", *ov.Enabled)
	}
}

// TestSaveManifestPluginsPreservesConfigOnToggle guards against toggle-only
// saves clobbering a previously-stored config definition override.
func TestSaveManifestPluginsPreservesConfigOnToggle(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/tap-web"
	const configJSON = `{"kind":"tool","name":"tap-web","display_name":"Custom Tap","description":"custom"}`

	// Pre-seed a config override row.
	if err := env.store.UpsertManifestPluginOverride(octx, config.ManifestPluginOverride{
		PluginID: pluginID,
		Config:   configJSON,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	// Toggle-only save: {id, enabled} with no Kind — must preserve existing config.
	body := map[string]any{"plugins": []map[string]any{{"id": pluginID, "enabled": false}}}
	rr := doRequest(t, env, "PATCH", "/api/manifest-plugins", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("save: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	ov, ok, err := env.store.GetManifestPluginOverride(octx, pluginID)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if !ok {
		t.Fatal("override row deleted by toggle-only save")
	}
	if ov.Config != configJSON {
		t.Fatalf("config clobbered by toggle-only save: got %q, want %q", ov.Config, configJSON)
	}
	if ov.Enabled == nil || *ov.Enabled != false {
		t.Fatalf("enabled not persisted: got %v, want explicit false", ov.Enabled)
	}
}

// TestSaveManifestPluginsRejectsDisablingEssential guards the harness: an
// essential builtin (rg/fd/mise back Grep/Glob/install) must not be disabled.
func TestSaveManifestPluginsRejectsDisablingEssential(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/rg" // essential: true in the builtin manifest

	body := map[string]any{"plugins": []map[string]any{{"id": pluginID, "enabled": false}}}
	rr := doRequest(t, env, "PATCH", "/api/manifest-plugins", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("disable essential: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	if _, ok, err := env.store.GetManifestPluginOverride(octx, pluginID); err != nil {
		t.Fatalf("get override: %v", err)
	} else if ok {
		t.Fatal("rejected save must not write an override row")
	}
}
