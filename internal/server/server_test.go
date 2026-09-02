package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	apitypes "github.com/CherryHQ/stella/api/types"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/account"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/controlplane"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/credential"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/inbox"
	"github.com/CherryHQ/stella/internal/library/recally"
	"github.com/CherryHQ/stella/internal/memory"
	lcmmemory "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	memprofile "github.com/CherryHQ/stella/internal/memory/profile"
	"github.com/CherryHQ/stella/internal/notify"
	oauthserver "github.com/CherryHQ/stella/internal/oidc"
	"github.com/CherryHQ/stella/internal/plugin/host"
	"github.com/CherryHQ/stella/internal/provisioning"
	"github.com/CherryHQ/stella/internal/server"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/internal/skill/access"
	"github.com/CherryHQ/stella/internal/webhook"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	_ "github.com/CherryHQ/stella/plugins/channels/discord"
	feishuplugin "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/qq"
	telegramplugin "github.com/CherryHQ/stella/plugins/channels/telegram"
	weixinplugin "github.com/CherryHQ/stella/plugins/channels/weixin"
)

type testAgentIDOccupancy struct{}

func (testAgentIDOccupancy) AgentIDOccupied(context.Context, string) (bool, error) { return false, nil }

// externalServerTestWorkspace maps each server fixture's temporary legacy Home
// layout. It is explicit test composition, never a production fallback.
type externalServerTestWorkspace struct{ root string }

func (w externalServerTestWorkspace) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	principal := filepath.Join(w.root, "users", req.UserID)
	if req.GroupID != "" {
		principal = filepath.Join(w.root, "users", "group-"+req.GroupID)
	}
	data, agentRoot := filepath.Join(principal, "data"), filepath.Join(principal, "agents", req.AgentID)
	if err := os.MkdirAll(filepath.Join(data, "assets"), 0o755); err != nil {
		return home.WorkspaceView{}, err
	}
	if err := os.MkdirAll(agentRoot, 0o755); err != nil {
		return home.WorkspaceView{}, err
	}
	return home.WorkspaceView{PrincipalRoot: principal, DataRoot: data, AgentRoot: agentRoot}, nil
}

func (w externalServerTestWorkspace) OpenRoot(ctx context.Context, req home.WorkspaceRequest, scope home.RootScope, _ home.RootAccess) (home.RootOperations, error) {
	view, err := w.WorkspaceView(ctx, req)
	if err != nil {
		return nil, err
	}
	dir := view.AgentRoot
	if scope == home.RootPrincipalData {
		dir = view.DataRoot
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return externalServerTestRoot{Root: root}, nil
}

type externalServerTestRoot struct{ *os.Root }

func (r externalServerTestRoot) Close() error { return r.Root.Close() }
func (r externalServerTestRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return r.Root.Stat(name)
}

func (r externalServerTestRoot) List(_ context.Context, name string, options home.ListOptions) ([]fs.DirEntry, error) {
	dir, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(options.Limit)
}

func (r externalServerTestRoot) Read(_ context.Context, name string, dst io.Writer, options home.ReadOptions) error {
	f, err := r.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(dst, io.LimitReader(f, options.MaxBytes))
	return err
}

func (r externalServerTestRoot) Write(_ context.Context, name string, src io.Reader, options home.WriteOptions) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return r.WriteFile(name, data, options.Mode)
}

func (r externalServerTestRoot) Upload(ctx context.Context, name string, src io.Reader, options home.WriteOptions) error {
	return r.Write(ctx, name, src, options)
}

func (r externalServerTestRoot) Mkdir(_ context.Context, name string, mode fs.FileMode, options home.MkdirOptions) error {
	if options.Parents {
		return r.MkdirAll(name, mode)
	}
	return r.Root.Mkdir(name, mode)
}

func (r externalServerTestRoot) Remove(_ context.Context, name string, options home.RemoveOptions) error {
	if options.Recursive {
		return r.RemoveAll(name)
	}
	return r.Root.Remove(name)
}

func (r externalServerTestRoot) Rename(_ context.Context, oldName, newName string, _ home.RenameOptions) error {
	return r.Root.Rename(oldName, newName)
}

// externalTestTransportOwnerDeletion preserves the HTTP fixture's historical
// database delete behavior. It is test-only: production uses Home OwnerDeletion.
type externalTestTransportOwnerDeletion struct {
	agents interface {
		DeleteAgent(context.Context, string) error
	}
}

func (d externalTestTransportOwnerDeletion) DeleteAgent(ctx context.Context, id, _ string) error {
	return d.agents.DeleteAgent(ctx, id)
}

func TestMain(m *testing.M) {
	// Lower bcrypt cost for the whole package before handing the run+exit to
	// dbtest.Main, which stops the shared embedded Postgres server afterward.
	auth.SetBcryptCostForTesting(bcrypt.MinCost)
	dbtest.Main(m)
}

// testUserDir adapts the OIDC user store to the Agent domain's UserDirectory
// port, mirroring the composition-root adapter for the external test package.
type testUserDir struct {
	users interface {
		GetUser(ctx context.Context, id string) (auth.User, error)
	}
}

func (d testUserDir) LookupUser(ctx context.Context, id string) (agentaccess.UserRef, error) {
	u, err := d.users.GetUser(ctx, id)
	if err != nil {
		return agentaccess.UserRef{}, err
	}
	return agentaccess.UserRef{ID: u.ID, Email: u.Email}, nil
}

func (d testUserDir) LookupUsers(ctx context.Context, ids []string) ([]agentaccess.UserRef, error) {
	out := make([]agentaccess.UserRef, 0, len(ids))
	for _, id := range ids {
		u, err := d.users.GetUser(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, agentaccess.UserRef{ID: u.ID, Email: u.Email})
	}
	return out, nil
}

type testEnv struct {
	srv         *server.Server
	db          *pgxpool.Pool
	store       config.Store
	pluginHost  *host.Host
	skillStore  *skill.POSIXStore
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

	stateStore := host.NewStateStore(db)
	channelRuntimeServices := host.NewChannelRuntimeServices()
	channelRuntimeServices.Set(context.Background(), testChannelHandler{}, dispatcher)
	phost := host.New(store,
		host.WithAuthService(host.NewAuthService(as)),
		host.WithNotificationService(dispatcher),
		host.WithStateStore(stateStore),
		host.WithChannelRuntimeServices(channelRuntimeServices),
	)
	if err := phost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	homeManager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatalf("home.NewWorkspaceManager: %v", err)
	}
	t.Cleanup(func() { _ = homeManager.Close() })
	skillStore, err := skill.NewPOSIXStore(db, homeManager)
	if err != nil {
		t.Fatalf("skill.NewPOSIXStore: %v", err)
	}

	oidcStore := appdb.NewOIDCStore(db)
	authSvc := auth.NewAuthService(db, oidcStore, oidcStore, oidcStore)
	sessionMgr, err := auth.NewSessionManager(oidcStore, "test-vault-key")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Build the same shared instances the composition root builds, so the test
	// server exercises the real, injected dependency set (no shadow construction).
	const baseURL = "http://localhost:25678"
	poolManager := agent.NewPoolManager(store, mem,
		agent.WithHomeWorkspace(homeManager),
		agent.WithProviderStreamBuilder(func(_, _, _ string) (providers.StreamFunc, error) {
			return func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
				return nil, nil
			}, nil
		}),
	)
	recallyStore := recally.NewStore(db)
	assetHome := t.TempDir()
	assetStore, err := asset.NewStore(assetHome, nil, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	credLog := slog.With("component", "admin-test")
	credPATStore := credential.NewPostgresStore(db)
	oauthStore := oauthserver.NewPostgresStore(db)
	credFrontDoor := credential.NewService(credential.Config{PATs: credPATStore, OAuth: oauthStore, Users: credPATStore, Logger: credLog})
	oauthAuthServer := oauthserver.NewService(oauthserver.Config{Store: oauthStore, Issuer: credFrontDoor, Logger: credLog})
	credSvc := connections.NewService(nil, sqlc.New(db), oauth.NewFlowStore(), baseURL)
	agentAccess := agentaccess.NewService(store, as)
	skillAccess := access.NewService(skillStore, agentAccess)
	projectStore := agent.NewProjectStore(db, agentAccess, agent.WithProjectHomeWorkspace(externalServerTestWorkspace{root: config.StellaHome()}))
	systemPromptBuilder, err := sessionaccess.NewSystemPromptBuilder(sessionaccess.SystemPromptDeps{
		Memory:    mem,
		Agents:    sessionaccess.ConfigPromptAgentStore{Store: store},
		Projects:  projectStore.Resolve,
		Workspace: externalServerTestWorkspace{root: config.StellaHome()},
		Plugins:   phost,
		Skills: func(ctx context.Context, build pkgplugins.SystemPromptContext, project *skill.ProjectSnapshot) (pkgplugins.SystemPromptSection, error) {
			return skill.BuildAuthorizedPromptSection(ctx, build, project, skillStore, skillAccess)
		},
	})
	if err != nil {
		t.Fatalf("sessionaccess.NewSystemPromptBuilder: %v", err)
	}
	sessionSvc, err := sessionaccess.NewService(mem, db, store, assetStore, agentAccess, sessionaccess.WithSystemPromptBuilder(systemPromptBuilder))
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	agentManagement := agentaccess.NewManagement(agentAccess, store, as, poolManager, testUserDir{users: oidcStore}, agent.NewAgentActivityStore(db), nil, nil, slog.With("component", "agent-management-test"), agentaccess.WithOwnerDeletion(externalTestTransportOwnerDeletion{agents: store}), agentaccess.WithAgentIDOccupancy(testAgentIDOccupancy{}))
	accountSvc := account.NewService(oidcStore, oidcStore, oidcStore, oidcStore, oidcStore, as, credFrontDoor, slog.With("component", "account-test"))
	provisioningSvc := provisioning.New(db, accountSvc, nil, slog.With("component", "provisioning-test"))
	memoryManagement := memorywrite.NewManagementService(db, mem)
	profileSvc := memprofile.NewService(db, mem, mem, memoryManagement, agentAccess, prompt.DefaultAgentSoul, slog.With("component", "profile-test"))
	webhookSvc, err := webhook.NewService(webhook.Config{Store: webhook.NewPostgresStore(db), Users: webhook.NewUserState(credPATStore), Access: webhook.NewUserAgentAccess(agentAccess)})
	if err != nil {
		t.Fatalf("webhook.NewService: %v", err)
	}
	deps := server.Deps{
		Pinger:              db,
		Group:               channel.NewGroupService(db, agentAccess, channel.NewRuntimeResolver(store), nil, nil),
		Account:             accountSvc,
		Profile:             profileSvc,
		ProjectStore:        projectStore,
		Inbox:               inbox.NewService(db),
		AgentAccess:         agentAccess,
		AgentManagement:     agentManagement,
		AgentSkillPolicy:    store,
		ToolOverrides:       agent.NewToolOverrideStore(db),
		SessionAccess:       sessionSvc,
		SkillAccess:         skillAccess,
		Skills:              skillStore,
		LinkCodes:           auth.NewLinkCodeStore(),
		PoolManager:         poolManager,
		PluginHost:          phost,
		WeixinRegistrar:     server.NewTestWeixinRegistrar(),
		BaseURL:             baseURL,
		Credentials:         credSvc,
		ControlPlane:        controlplane.NewService(store, phost, poolManager, credSvc, slog.With("component", "controlplane-test")),
		Webhooks:            webhookSvc,
		Email:               email.NewService(nil, sqlc.New(db)),
		Share:               sharepkg.NewService(sqlc.New(db), mem, recallyStore, assetHome, baseURL, sharepkg.WithHomeWorkspace(externalServerTestWorkspace{root: config.StellaHome()}), sharepkg.WithAgentAccess(agentAccess)),
		Assets:              assetStore,
		Recally:             recally.NewService(recallyStore, t.TempDir()),
		CredentialFrontDoor: credFrontDoor,
		OAuthAuthServer:     oauthAuthServer,
		Provisioning:        provisioningSvc,
		OIDC: server.OIDCDeps{
			AuthSvc:    authSvc,
			SessionMgr: sessionMgr,
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
		skillStore:  skillStore,
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
	for _, want := range []string{"PluginHost", "BaseURL"} {
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
	if len(providers) != 0 {
		t.Fatalf("providers = %v, want none before explicit configuration", providers)
	}
}

func TestModelCatalogAdminEndpointsAndProviderCAS(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/model-catalog/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog providers status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var catalog struct {
		Providers []struct {
			Id        string `json:"id"`
			ApiType   string `json:"api_type"`
			BaseURL   string `json:"base_url"`
			Supported bool   `json:"supported"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &catalog); err != nil {
		t.Fatalf("decode catalog providers: %v", err)
	}
	foundOpenAI := false
	for _, provider := range catalog.Providers {
		if provider.Id == "google" || provider.Id == "amazon-bedrock" {
			t.Fatalf("unsupported provider %q returned by default listing", provider.Id)
		}
		if provider.Id == "openai" {
			foundOpenAI = true
			if provider.ApiType != "openai-response" || provider.BaseURL != "https://api.openai.com/v1" || !provider.Supported {
				t.Fatalf("openai catalog mapping = %+v", provider)
			}
		}
	}
	if !foundOpenAI {
		t.Fatal("catalog providers omitted openai")
	}
	rr = doRequest(t, env, "GET", "/api/model-catalog/providers?include_unsupported=true", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog providers all status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"id":"google"`) {
		t.Fatal("include_unsupported listing omitted google")
	}

	rr = doRequest(t, env, "GET", "/api/model-catalog/models", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog models status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var catalogModels apitypes.CatalogModelList
	if err := json.Unmarshal(parseResponse(t, rr).Data, &catalogModels); err != nil {
		t.Fatalf("decode canonical catalog models: %v", err)
	}
	foundOpenAIModel := false
	for _, model := range catalogModels.Models {
		if model.Id == "openai/gpt-4o" {
			foundOpenAIModel = true
			break
		}
	}
	if !foundOpenAIModel {
		t.Fatal("complete catalog model listing omitted openai/gpt-4o")
	}

	rr = doRequest(t, env, "POST", "/api/providers", map[string]any{
		"id": "catalog-defaults", "name": "Catalog defaults", "enabled": true,
		"api_key": "sk-test", "catalog_id": "openai",
		"models": map[string]any{"gpt-4o": map[string]any{"enabled": true}},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create catalog provider = %d, body = %s", rr.Code, rr.Body.String())
	}
	var catalogProvider apitypes.Provider
	if err := json.Unmarshal(parseResponse(t, rr).Data, &catalogProvider); err != nil {
		t.Fatalf("decode catalog provider: %v", err)
	}
	if catalogProvider.Type != "openai-response" || catalogProvider.BaseUrl != "https://api.openai.com/v1" || catalogProvider.ModelPolicy == nil || *catalogProvider.ModelPolicy != "allow_all" {
		t.Fatalf("catalog defaults = %+v", catalogProvider)
	}
	if catalogProvider.Models == nil || (*catalogProvider.Models)["gpt-4o"].Enabled == nil || !*(*catalogProvider.Models)["gpt-4o"].Enabled {
		t.Fatalf("sparse model override lost: %+v", catalogProvider.Models)
	}
	rr = doRequest(t, env, "GET", "/api/providers/catalog-defaults/models", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list catalog provider models = %d, body = %s", rr.Code, rr.Body.String())
	}
	var providerCatalogModels apitypes.ProviderModelList
	if err := json.Unmarshal(parseResponse(t, rr).Data, &providerCatalogModels); err != nil {
		t.Fatalf("decode catalog provider models: %v", err)
	}
	foundCatalogModel := false
	for _, model := range providerCatalogModels.Models {
		if model.Id != "gpt-4o" {
			continue
		}
		foundCatalogModel = true
		if model.Catalog == nil || model.Catalog.ContextWindow == nil || *model.Catalog.ContextWindow <= 0 {
			t.Fatalf("catalog model projection = %+v", model.Catalog)
		}
	}
	if !foundCatalogModel {
		t.Fatal("effective catalog model list omitted gpt-4o")
	}

	rr = doRequest(t, env, "POST", "/api/providers", map[string]any{
		"id": "custom-catalog-match", "type": "openai-response", "name": "Custom catalog match", "enabled": true,
		"api_key": "sk-test", "base_url": "https://gateway.example/v1",
		"models": map[string]any{"gateway-gpt": map[string]any{
			"enabled": true, "catalogModel": "openai/gpt-4o",
		}},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create custom provider with global catalog match = %d, body = %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, "GET", "/api/providers/custom-catalog-match/models", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"id":"openai/gpt-4o"`) {
		t.Fatalf("custom provider catalog projection = %d, body = %s", rr.Code, rr.Body.String())
	}

	before := doRequest(t, env, "GET", "/api/providers", nil)
	rr = doRequest(t, env, "POST", "/api/providers/probe", map[string]any{"api_type": "not-a-provider", "api_key": "sk-invalid", "base_url": "https://example.invalid"})
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "error") {
		t.Fatalf("invalid probe = %d, body = %s", rr.Code, rr.Body.String())
	}
	after := doRequest(t, env, "GET", "/api/providers", nil)
	var beforeList, afterList struct {
		Providers []json.RawMessage `json:"providers"`
	}
	_ = json.Unmarshal(parseResponse(t, before).Data, &beforeList)
	_ = json.Unmarshal(parseResponse(t, after).Data, &afterList)
	if len(beforeList.Providers) != len(afterList.Providers) {
		t.Fatalf("probe changed provider count: before=%d after=%d", len(beforeList.Providers), len(afterList.Providers))
	}

	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "catalog-regular", auth.RoleUser)
	rr = doRequestWithSession(t, env.srv, userToken, "GET", "/api/model-catalog/status", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("regular catalog status = %d, want 403", rr.Code)
	}

	body := map[string]any{"id": "cas-provider", "type": "openai", "name": "CAS", "enabled": true, "api_key": "sk-test"}
	rr = doRequest(t, env, "POST", "/api/providers", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create CAS provider = %d", rr.Code)
	}
	var provider apitypes.Provider
	if err := json.Unmarshal(parseResponse(t, rr).Data, &provider); err != nil {
		t.Fatalf("decode created provider: %v", err)
	}
	if provider.Version == nil || *provider.Version == "" {
		t.Fatal("created provider has no version")
	}
	rr = doRequest(t, env, "PATCH", "/api/providers/cas-provider", map[string]any{"name": "stale", "expected_version": "2000-01-01T00:00:00Z"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale provider patch = %d, want 409, body = %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, "PATCH", "/api/providers/cas-provider", map[string]any{"name": "invalid", "expected_version": "not-a-version"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid provider version = %d, want 400, body = %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, "DELETE", "/api/providers/cas-provider?expected_version=2000-01-01T00%3A00%3A00Z", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale provider delete = %d, want 409, body = %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, "DELETE", "/api/providers/cas-provider?expected_version="+url.QueryEscape(*provider.Version), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("provider delete = %d, want 204, body = %s", rr.Code, rr.Body.String())
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
		path          string
		propertyNames []string
	}{
		{path: "/api/plugins/channel/telegram/config-schema", propertyNames: []string{"token", "allow_group", "allow_dm", "allow_unlinked_dm", "guest_message_limit_per_minute", "guest_max_per_channel", "guest_retention_days", "require_mention"}},
		{path: "/api/plugins/channel/discord/config-schema", propertyNames: []string{"token", "allow_group", "allow_dm", "allow_unlinked_dm", "guest_message_limit_per_minute", "guest_max_per_channel", "guest_retention_days", "require_mention"}},
		{path: "/api/plugins/channel/qq/config-schema", propertyNames: []string{"app_id"}},
		{path: "/api/plugins/channel/feishu/config-schema", propertyNames: []string{"app_id", "allow_group", "allow_dm", "allow_unlinked_dm", "guest_message_limit_per_minute", "guest_max_per_channel", "guest_retention_days", "require_mention"}},
		{path: "/api/plugins/channel/weixin/config-schema", propertyNames: []string{"bot_token"}},
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
			for _, propertyName := range tt.propertyNames {
				if _, ok := props[propertyName]; !ok {
					t.Fatalf("expected %q property in schema: %#v", propertyName, schema)
				}
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

func TestChannelCreateIsInsertOnlyAndPatchIsUpdateOnly(t *testing.T) {
	env := setupAdmin(t)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)
	body := map[string]any{
		"id": "telegram-method-contract", "type": "telegram", "enabled": false,
		"config": `{"token":"tg-token"}`,
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/channels", body); rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/channels", body); rr.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPatch, "/api/channels/missing-channel", map[string]any{
		"type": "telegram", "enabled": false, "config": `{"token":"tg-token"}`,
	}); rr.Code != http.StatusNotFound {
		t.Fatalf("missing patch = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

// A create without an id is the normal path: the client never invents one.
func TestChannelCreateGeneratesIDAndName(t *testing.T) {
	env := setupAdmin(t)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)
	enableChannelPlugin(t, env, pkgchannel.PlatformWeixin)

	createChannel := func(t *testing.T, body map[string]any) channelPayload {
		t.Helper()
		rr := doRequest(t, env, http.MethodPost, "/api/channels", body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
		}
		var saved channelPayload
		if err := json.Unmarshal(parseResponse(t, rr).Data, &saved); err != nil {
			t.Fatalf("unmarshal channel: %v", err)
		}
		return saved
	}

	t.Run("id is generated when omitted", func(t *testing.T) {
		saved := createChannel(t, map[string]any{"type": "telegram", "name": "Explicit Name"})
		if saved.ID == "" || saved.ID == "telegram" {
			t.Fatalf("generated id = %q, want a non-empty id distinct from the type", saved.ID)
		}
		if saved.Name != "Explicit Name" {
			t.Fatalf("name = %q, want the supplied name", saved.Name)
		}
		// The generated id must address the row it created.
		if rr := doRequest(t, env, http.MethodGet, "/api/channels/"+saved.ID, nil); rr.Code != http.StatusOK {
			t.Fatalf("get generated channel = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
		}
	})

	t.Run("name defaults to type-suffix", func(t *testing.T) {
		saved := createChannel(t, map[string]any{"type": "telegram"})
		if !strings.HasPrefix(saved.Name, "telegram-") || saved.Name == "telegram-" {
			t.Fatalf("default name = %q, want a %q prefix plus a suffix", saved.Name, "telegram-")
		}
	})

	t.Run("weixin without an id gets the singleton id", func(t *testing.T) {
		saved := createChannel(t, map[string]any{"type": "weixin"})
		if saved.ID != pkgchannel.PlatformWeixin {
			t.Fatalf("weixin id = %q, want %q", saved.ID, pkgchannel.PlatformWeixin)
		}
	})

	t.Run("an explicit id is still honored and still conflicts", func(t *testing.T) {
		saved := createChannel(t, map[string]any{"type": "telegram", "id": "telegram-pinned"})
		if saved.ID != "telegram-pinned" {
			t.Fatalf("id = %q, want the supplied id", saved.ID)
		}
		rr := doRequest(t, env, http.MethodPost, "/api/channels", map[string]any{
			"type": "telegram", "id": "telegram-pinned",
		})
		if rr.Code != http.StatusConflict {
			t.Fatalf("duplicate create = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
		}
	})

	t.Run("type is still required", func(t *testing.T) {
		rr := doRequest(t, env, http.MethodPost, "/api/channels", map[string]any{"name": "nameless"})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create without type = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
		}
	})
}

type channelPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func TestUpdateTelegramChannelUsesPluginHostRuntime(t *testing.T) {
	env := setupAdmin(t)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)

	rr := doRequest(t, env, "POST", "/api/channels", map[string]any{
		"id": "telegram", "type": "telegram", "enabled": true,
		"config": `{"token":"tg-token","enable_notify":true}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	rr = doRequest(t, env, "PATCH", "/api/channels/telegram", map[string]any{
		"enabled": true, "config": `{"token":"tg-token","enable_notify":true}`,
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

	rr := doRequest(t, env, "POST", "/api/channels", map[string]any{
		"id": "qq", "type": "qq", "enabled": true,
		"config": `{"app_id":"qq-app","app_secret":"qq-secret","enable_notify":true}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	rr = doRequest(t, env, "PATCH", "/api/channels/qq", map[string]any{
		"enabled": true, "config": `{"app_id":"qq-app","app_secret":"qq-secret","enable_notify":true}`,
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

	rr := doRequest(t, env, "POST", "/api/channels", map[string]any{
		"id": "feishu", "type": "feishu", "enabled": true,
		"config": `{"app_id":"fs-app","app_secret":"fs-secret","encrypt_key":"enc","verification_token":"verify","enable_notify":true,"groups":{"oc_123":{"system_prompt":"be brief"}}}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	rr = doRequest(t, env, "PATCH", "/api/channels/feishu", map[string]any{
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

	rr := doRequest(t, env, "POST", "/api/channels", map[string]any{
		"id": "weixin", "type": "weixin", "enabled": true,
		"config": `{"bot_token":"wx-token","base_url":"https://wx.example","bot_id":"bot-1","user_id":"user-1","enable_notify":true}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	rr = doRequest(t, env, "PATCH", "/api/channels/weixin", map[string]any{
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
	// Discord deliberately gets no plugin row: a platform is usable unless an
	// admin switched it off, so a channel must be public without one.
	if err := env.store.UpsertChannel(octx, config.Channel{
		ID:      "discord-stella",
		Type:    pkgchannel.PlatformDiscord,
		AgentID: stellaID,
		Enabled: true,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel discord-stella: %v", err)
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
	if _, ok := byID["discord-stella"]; !ok {
		t.Fatalf("discord channel without a plugin row should be public: %#v", channels)
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

// A channel's platform is fixed at creation. Retyping one is how an ordinary
// owner could otherwise mint a second Weixin channel and, through the Weixin
// credential mirror, take over the deployment-wide Weixin plugin row.
func TestUpdateChannelRejectsRetyping(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	if err := env.store.UpsertChannel(octx, config.Channel{
		ID:      "tg-1",
		Type:    pkgchannel.PlatformTelegram,
		Enabled: false,
		Config:  `{}`,
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	rr := doRequest(t, env, "PATCH", "/api/channels/tg-1", map[string]any{
		"type": pkgchannel.PlatformWeixin,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("retype status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	ch, err := env.store.GetChannel(octx, "tg-1")
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if ch.Type != pkgchannel.PlatformTelegram {
		t.Fatalf("type = %q, want %q", ch.Type, pkgchannel.PlatformTelegram)
	}
	if _, err := env.store.GetChannel(octx, pkgchannel.PlatformWeixin); err == nil {
		t.Fatal("a second weixin channel must not exist")
	}

	// Restating the current type is not a change and must still be accepted.
	rr = doRequest(t, env, "PATCH", "/api/channels/tg-1", map[string]any{
		"type": pkgchannel.PlatformTelegram,
		"name": "renamed",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("same-type update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

// channelConfigEquals compares stored config JSON by value: the server round-trips
// config through a map, so key order is not stable.
func channelConfigEquals(t *testing.T, got, want string) bool {
	t.Helper()
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatalf("unmarshal stored config %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantMap); err != nil {
		t.Fatalf("unmarshal want config %q: %v", want, err)
	}
	return reflect.DeepEqual(gotMap, wantMap)
}

// A PATCH that omits config must not wipe the channel's stored credentials:
// config is tri-state like agent_id (absent keeps, explicit value replaces).
func TestUpdateChannelConfigIsTriState(t *testing.T) {
	env := setupAdmin(t)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)
	octx := context.Background()

	const original = `{"token":"tg-token","enable_notify":true}`
	rr := doRequest(t, env, http.MethodPost, "/api/channels", map[string]any{
		"id": "telegram", "type": "telegram", "config": original,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	agentID := findStellaID(t, env)
	rr = doRequest(t, env, http.MethodPatch, "/api/channels/telegram", map[string]any{"agent_id": agentID})
	if rr.Code != http.StatusOK {
		t.Fatalf("agent-only patch status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	ch, err := env.store.GetChannel(octx, pkgchannel.PlatformTelegram)
	if err != nil {
		t.Fatalf("GetChannel telegram: %v", err)
	}
	if ch.AgentID != agentID {
		t.Fatalf("agent_id = %q, want %q", ch.AgentID, agentID)
	}
	if !channelConfigEquals(t, ch.Config, original) {
		t.Fatalf("omitted config was overwritten: got %q, want %q", ch.Config, original)
	}

	const replacement = `{"token":"tg-token-2"}`
	rr = doRequest(t, env, http.MethodPatch, "/api/channels/telegram", map[string]any{"config": replacement})
	if rr.Code != http.StatusOK {
		t.Fatalf("config patch status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	ch, err = env.store.GetChannel(octx, pkgchannel.PlatformTelegram)
	if err != nil {
		t.Fatalf("GetChannel telegram: %v", err)
	}
	if !channelConfigEquals(t, ch.Config, replacement) {
		t.Fatalf("config = %q, want %q", ch.Config, replacement)
	}
	if ch.AgentID != agentID {
		t.Fatalf("config patch dropped agent_id: got %q, want %q", ch.AgentID, agentID)
	}

	rr = doRequest(t, env, http.MethodPatch, "/api/channels/telegram", map[string]any{"config": ""})
	if rr.Code != http.StatusOK {
		t.Fatalf("clear config status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	ch, err = env.store.GetChannel(octx, pkgchannel.PlatformTelegram)
	if err != nil {
		t.Fatalf("GetChannel telegram: %v", err)
	}
	if ch.Config != `{}` {
		t.Fatalf("explicit empty config = %q, want {}", ch.Config)
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

	// Every authenticated user, including an admin, lands on /agents.
	rr := doRequest(t, env, "GET", "/", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/agents" {
		t.Errorf("Location = %q, want %q", loc, "/agents")
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
		"/settings/providers", "/agents", "/settings/channels",
		"/settings/users", "/sessions", "/scheduler",
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
// the q parameter. A real search against skill.sh is NOT tested here — that
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

// toggleManifestPlugin flips one plugin's enable switch. The route addresses a
// single plugin on purpose: a request that carried the whole list made every
// toggle a deployment-wide write.
func toggleManifestPlugin(t *testing.T, env *testEnv, id string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, env, "PUT", "/api/manifest-plugins/"+id+"/enabled", map[string]any{"enabled": enabled})
}

// saveManifestDefinition writes one plugin's definition, naming the fields the
// request takes ownership of.
func saveManifestDefinition(t *testing.T, env *testEnv, id string, plugin map[string]any, fields []string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"plugin": plugin}
	if fields != nil {
		body["fields"] = fields
	}
	return doRequest(t, env, "PATCH", "/api/manifest-plugins/"+id, body)
}

// TestToggleManifestPluginPreservesSessionEnvVaultKey guards against the enable
// toggle clobbering the session_env_vault_key. The toggle payload only carries
// the enable flag, so the handler must read the existing override row and
// preserve any session env binding instead of overwriting it with "".
func TestToggleManifestPluginPreservesSessionEnvVaultKey(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	// tool/lark-cli defaults to enabled=true in the builtin manifest (and is not
	// essential, so it can be toggled). Pre-seed an override row that binds a
	// session env vault key.
	const pluginID = "tool/lark-cli"
	const vaultKey = "vault/session/larkcli"
	if err := env.store.UpsertManifestPluginOverride(octx, config.ManifestPluginOverride{
		PluginID:           pluginID,
		SessionEnvVaultKey: vaultKey,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	// Disable the plugin (enabled diverges from default).
	if rr := toggleManifestPlugin(t, env, pluginID, false); rr.Code != http.StatusOK {
		t.Fatalf("toggle: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
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
		t.Fatalf("a toggle must not store a config override: got %q", ov.Config)
	}

	// Toggle back to the default (enabled=true). The row must survive because a
	// session env binding still exists; only the enable override clears to nil.
	if rr := toggleManifestPlugin(t, env, pluginID, true); rr.Code != http.StatusOK {
		t.Fatalf("toggle back: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
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

// TestToggleManifestPluginPreservesConfig guards against a toggle clobbering a
// previously-stored config definition override.
func TestToggleManifestPluginPreservesConfig(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/lark-cli"
	const configJSON = `{"kind":"tool","name":"lark-cli","display_name":"Custom Lark","description":"custom"}`

	// Pre-seed a config override row.
	if err := env.store.UpsertManifestPluginOverride(octx, config.ManifestPluginOverride{
		PluginID: pluginID,
		Config:   configJSON,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	if rr := toggleManifestPlugin(t, env, pluginID, false); rr.Code != http.StatusOK {
		t.Fatalf("toggle: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	ov, ok, err := env.store.GetManifestPluginOverride(octx, pluginID)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if !ok {
		t.Fatal("override row deleted by a toggle")
	}
	if ov.Config != configJSON {
		t.Fatalf("config clobbered by a toggle: got %q, want %q", ov.Config, configJSON)
	}
	if ov.Enabled == nil || *ov.Enabled != false {
		t.Fatalf("enabled not persisted: got %v, want explicit false", ov.Enabled)
	}
}

// A mutation addresses one plugin. When one request carried the whole list, the
// server rewrote every row from it — so toggling A rewrote B, and a row written
// before overrides went sparse was converted by an admin who never touched that
// plugin. The stored bytes of the bystander are the assertion.
func TestMutatingOnePluginLeavesAnotherRowByteIdentical(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const bystander = "tool/gh"
	const legacyJSON = `{"kind":"tool","name":"gh","display_name":"Our gh","description":"pinned long ago"}`
	if err := env.store.UpsertManifestPluginOverride(octx, config.ManifestPluginOverride{
		PluginID: bystander,
		Config:   legacyJSON,
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	if rr := toggleManifestPlugin(t, env, "tool/lark-cli", false); rr.Code != http.StatusOK {
		t.Fatalf("toggle: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	edited := manifestPluginByID(t, env, "tool/lark-cli")
	edited["display_name"] = "Lark (ours)"
	if rr := saveManifestDefinition(t, env, "tool/lark-cli", edited, []string{"display_name"}); rr.Code != http.StatusOK {
		t.Fatalf("save: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	ov, ok, err := env.store.GetManifestPluginOverride(octx, bystander)
	if err != nil || !ok {
		t.Fatalf("get bystander override: ok=%v err=%v", ok, err)
	}
	if ov.Config != legacyJSON {
		t.Fatalf("a bystander's row was rewritten: got %q, want %q", ov.Config, legacyJSON)
	}
}

// TestToggleManifestPluginRejectsDisablingEssential guards the harness: an
// essential builtin (rg/fd/mise back Grep/Glob/install) must not be disabled.
func TestToggleManifestPluginRejectsDisablingEssential(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/rg" // essential: true in the builtin manifest

	rr := toggleManifestPlugin(t, env, pluginID, false)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("disable essential: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	// The rejection has to come from Essential, not from a blanket "builtins are
	// untouchable" rule: that distinction is the whole point of the guard.
	if body := rr.Body.String(); !strings.Contains(body, "essential") {
		t.Fatalf("rejection reason = %s, want it to name essential", body)
	}
	if _, ok, err := env.store.GetManifestPluginOverride(octx, pluginID); err != nil {
		t.Fatalf("get override: %v", err)
	} else if ok {
		t.Fatal("a rejected toggle must not write an override row")
	}

	// ...and a non-essential builtin stays an admin's call.
	const optionalID = "tool/lark-cli"
	if rr := toggleManifestPlugin(t, env, optionalID, false); rr.Code != http.StatusOK {
		t.Fatalf("disable non-essential builtin: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if ov, ok, err := env.store.GetManifestPluginOverride(octx, optionalID); err != nil || !ok {
		t.Fatalf("non-essential toggle not persisted: ok=%v err=%v", ok, err)
	} else if ov.Enabled == nil || *ov.Enabled {
		t.Fatalf("override enabled = %v, want a recorded false", ov.Enabled)
	}
}

// TestDeleteManifestPluginRemovesCustomOnly covers the delete an added CLI tool
// needs: an admin-added plugin lives entirely in its override row and goes away
// with it, while a builtin is refused because the next resolve would bring it
// back anyway.
func TestDeleteManifestPluginRemovesCustomOnly(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const customID = "tool/my-cli"
	if rr := saveManifestDefinition(t, env, customID, map[string]any{
		"display_name": "My CLI",
		"description":  "",
		"enabled":      true,
		"binaries":     []map[string]any{{"name": "my-cli", "tool": "github:owner/repo"}},
	}, nil); rr.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if _, ok, err := env.store.GetManifestPluginOverride(octx, customID); err != nil || !ok {
		t.Fatalf("custom plugin not persisted: ok=%v err=%v", ok, err)
	}

	if rr := doRequest(t, env, "DELETE", "/api/manifest-plugins/tool/my-cli", nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete custom: status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	if _, ok, err := env.store.GetManifestPluginOverride(octx, customID); err != nil {
		t.Fatalf("get override: %v", err)
	} else if ok {
		t.Fatal("override row survived the delete")
	}

	// A second delete has nothing to remove.
	if rr := doRequest(t, env, "DELETE", "/api/manifest-plugins/tool/my-cli", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("delete missing: status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}

	// A builtin ships with the server: disable it, don't delete it.
	if rr := doRequest(t, env, "DELETE", "/api/manifest-plugins/tool/lark-cli", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("delete builtin: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestListManifestPluginsMarksBuiltin covers the flag the settings UI uses to
// decide whether a plugin may be removed at all.
func TestListManifestPluginsMarksBuiltin(t *testing.T) {
	env := setupAdmin(t)

	if rr := saveManifestDefinition(t, env, "tool/my-cli", map[string]any{
		"display_name": "My CLI", "description": "", "enabled": true,
		"binaries": []map[string]any{{"name": "my-cli", "tool": "github:owner/repo"}},
	}, nil); rr.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	rr := doRequest(t, env, "GET", "/api/manifest-plugins", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want 200", rr.Code)
	}
	var resp struct {
		Plugins []struct {
			ID      string `json:"id"`
			Builtin bool   `json:"builtin"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range resp.Plugins {
		seen[p.ID] = p.Builtin
	}
	if builtin, ok := seen["tool/lark-cli"]; !ok || !builtin {
		t.Fatalf("tool/lark-cli builtin = %v (present=%v), want true", builtin, ok)
	}
	if builtin, ok := seen["tool/my-cli"]; !ok || builtin {
		t.Fatalf("tool/my-cli builtin = %v (present=%v), want false", builtin, ok)
	}
}

// manifestPluginByID reads one plugin out of the merged manifest as a raw JSON
// object, which is what the settings UI edits and sends back.
func manifestPluginByID(t *testing.T, env *testEnv, id string) map[string]any {
	t.Helper()
	rr := doRequest(t, env, "GET", "/api/manifest-plugins", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want 200", rr.Code)
	}
	var resp struct {
		Plugins []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range resp.Plugins {
		if p["id"] == id {
			return p
		}
	}
	t.Fatalf("plugin %q not in the merged manifest", id)
	return nil
}

// Editing one field of a builtin must store that field and nothing else. A row
// holding the whole definition would freeze the plugin at the version that was
// running when someone edited it: every later release's improvement to the other
// fields would arrive in the binary and be discarded by the row.
func TestSaveManifestPluginStoresOnlyTheNamedField(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/lark-cli"
	plugin := manifestPluginByID(t, env, pluginID)
	plugin["display_name"] = "Lark (ours)"

	if rr := saveManifestDefinition(t, env, pluginID, plugin, []string{"display_name"}); rr.Code != http.StatusOK {
		t.Fatalf("save: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	ov, ok, err := env.store.GetManifestPluginOverride(octx, pluginID)
	if err != nil || !ok {
		t.Fatalf("get override: ok=%v err=%v", ok, err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(ov.Config), &stored); err != nil {
		t.Fatalf("decode stored override %q: %v", ov.Config, err)
	}
	if stored["$sparse"] != true {
		t.Fatalf("stored override = %s, want the sparse-format marker", ov.Config)
	}
	delete(stored, "$sparse")
	if len(stored) != 1 || stored["display_name"] != "Lark (ours)" {
		t.Fatalf("stored override = %s, want display_name alone", ov.Config)
	}

	// And the merged view names the owned field, so the editor can mark it and
	// offer to hand that one back.
	merged := manifestPluginByID(t, env, pluginID)
	if merged["display_name"] != "Lark (ours)" {
		t.Fatalf("merged plugin = %v, want the edit", merged)
	}
	owned, _ := merged["overridden_fields"].([]any)
	if len(owned) != 1 || owned[0] != "display_name" {
		t.Fatalf("overridden_fields = %v, want display_name alone", merged["overridden_fields"])
	}
}

// Issue #963's second acceptance criterion: an admin who pinned two fields can
// hand back one of them and keep the other.
func TestResetManifestPluginFieldReleasesOneFieldOnly(t *testing.T) {
	env := setupAdmin(t)

	const pluginID = "tool/lark-cli"
	shipped := manifestPluginByID(t, env, pluginID)

	edited := manifestPluginByID(t, env, pluginID)
	edited["display_name"] = "Lark (ours)"
	edited["description"] = "ours too"
	if rr := saveManifestDefinition(t, env, pluginID, edited, []string{"display_name", "description"}); rr.Code != http.StatusOK {
		t.Fatalf("save: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	if rr := doRequest(t, env, "POST", "/api/manifest-plugins/tool/lark-cli/reset",
		map[string]any{"field": "description"}); rr.Code != http.StatusOK {
		t.Fatalf("reset field: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	after := manifestPluginByID(t, env, pluginID)
	if after["description"] != shipped["description"] {
		t.Errorf("description = %v, want the shipped %v", after["description"], shipped["description"])
	}
	if after["display_name"] != "Lark (ours)" {
		t.Errorf("display_name = %v, want the other pin to hold", after["display_name"])
	}
	owned, _ := after["overridden_fields"].([]any)
	if len(owned) != 1 || owned[0] != "display_name" {
		t.Fatalf("overridden_fields = %v, want display_name alone", after["overridden_fields"])
	}

	// A field nobody owns has nothing to hand back, and neither does a field that
	// is not part of a definition at all.
	if rr := doRequest(t, env, "POST", "/api/manifest-plugins/tool/lark-cli/reset",
		map[string]any{"field": "description"}); rr.Code != http.StatusNotFound {
		t.Fatalf("reset an unowned field: status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, "POST", "/api/manifest-plugins/tool/lark-cli/reset",
		map[string]any{"field": "enabled"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("reset a non-definition field: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

// A plugin's stable ID and editable name are different things. Every write must
// address the row by ID; routing by name would miss it and create a second row.
func TestWritesAddressAPluginWhoseNameIsNotItsID(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/historical-id"
	if rr := saveManifestDefinition(t, env, pluginID, map[string]any{
		"name":         "current-name",
		"display_name": "Current name",
		"description":  "",
		"enabled":      true,
		"binaries":     []map[string]any{{"name": "current-name", "tool": "github:owner/repo"}},
	}, nil); rr.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	if rr := toggleManifestPlugin(t, env, pluginID, false); rr.Code != http.StatusOK {
		t.Fatalf("disable: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	after := manifestPluginByID(t, env, pluginID)
	if after["name"] != "current-name" || after["enabled"] != false {
		t.Fatalf("plugin = %v, want stable ID with edited name and disabled state", after)
	}
	if _, _, err := env.store.GetManifestPluginOverride(octx, pluginID); err != nil {
		t.Fatalf("get override: %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/manifest-plugins", nil)
	var resp struct {
		Plugins []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range resp.Plugins {
		if p["id"] == "tool/current-name" {
			t.Fatalf("a second plugin appeared at %v; the write addressed the name instead of the ID", p["id"])
		}
	}
}

// kind and essential belong to the server. kind is part of the plugin's identity
// — the ID is prefixed with it and the runtime reloads by it — and essential is
// the server's statement that disabling this plugin breaks a core tool, which the
// disable check reads from the shipped definition regardless. An override that
// claimed either would only produce a UI disagreeing with the API.
func TestKindAndEssentialCannotBeOverridden(t *testing.T) {
	env := setupAdmin(t)

	const pluginID = "tool/lark-cli"
	shipped := manifestPluginByID(t, env, pluginID)

	// essential is omitempty, so a plugin that is not essential simply has no key.
	wasEssential, _ := shipped["essential"].(bool)
	edited := manifestPluginByID(t, env, pluginID)
	edited["essential"] = !wasEssential
	if rr := saveManifestDefinition(t, env, pluginID, edited, []string{"essential"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("own essential: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := saveManifestDefinition(t, env, pluginID, edited, []string{"kind"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("own kind: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}

	// A body that addresses a different kind than the URL is addressing something
	// other than what it asked for.
	mismatched := manifestPluginByID(t, env, pluginID)
	mismatched["kind"] = "hook"
	if rr := saveManifestDefinition(t, env, pluginID, mismatched, []string{"display_name"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("kind that disagrees with the URL: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}

	after := manifestPluginByID(t, env, pluginID)
	if stillEssential, _ := after["essential"].(bool); stillEssential != wasEssential || after["kind"] != shipped["kind"] {
		t.Fatalf("plugin = %v, want kind and essential still the server's", after)
	}
}

// Reset is the way back from a customization. It drops the definition override
// and leaves the enable switch alone — "stop diverging" is not "turn off".
func TestResetManifestPluginRestoresTheShippedDefinition(t *testing.T) {
	env := setupAdmin(t)
	octx := context.Background()

	const pluginID = "tool/lark-cli"
	shipped := manifestPluginByID(t, env, pluginID)
	shippedName := shipped["display_name"]

	edited := manifestPluginByID(t, env, pluginID)
	edited["display_name"] = "Lark (ours)"
	if rr := saveManifestDefinition(t, env, pluginID, edited, []string{"display_name"}); rr.Code != http.StatusOK {
		t.Fatalf("save: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := toggleManifestPlugin(t, env, pluginID, false); rr.Code != http.StatusOK {
		t.Fatalf("disable: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	if rr := doRequest(t, env, "POST", "/api/manifest-plugins/tool/lark-cli/reset", nil); rr.Code != http.StatusOK {
		t.Fatalf("reset: status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	after := manifestPluginByID(t, env, pluginID)
	if after["display_name"] != shippedName {
		t.Errorf("display_name = %v, want the shipped %v", after["display_name"], shippedName)
	}
	if owned, _ := after["overridden_fields"].([]any); len(owned) != 0 {
		t.Errorf("overridden_fields = %v, want none after the reset", owned)
	}
	if after["enabled"] != false {
		t.Errorf("enabled = %v, want the reset to leave it disabled", after["enabled"])
	}
	ov, ok, err := env.store.GetManifestPluginOverride(octx, pluginID)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if !ok || ov.Enabled == nil || *ov.Enabled {
		t.Fatalf("the enable override did not survive the reset: ok=%v enabled=%v", ok, ov.Enabled)
	}
	if ov.Config != "" {
		t.Errorf("config override survived the reset: %q", ov.Config)
	}

	// Nothing to reset, and nothing to reset *to*.
	if rr := doRequest(t, env, "POST", "/api/manifest-plugins/tool/lark-cli/reset", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("reset an unedited builtin: status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, "POST", "/api/manifest-plugins/tool/my-cli/reset", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("reset a non-builtin: status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /api/channels/{id}/bind — the one channel write open to a non-admin.
// ---------------------------------------------------------------------------

// bindTestEnv seeds a telegram channel plus a system-scope agent (usable by any
// signed-in user) and a restricted agent (usable by nobody but an admin), and
// returns a regular user's session token.
func setupChannelBindEnv(t *testing.T, channelID string, enabled bool) (*testEnv, string) {
	t.Helper()
	env := setupAdmin(t)
	enableChannelPlugin(t, env, pkgchannel.PlatformTelegram)
	ctx := context.Background()
	for _, agent := range []config.Agent{
		{ID: "bind-open", Name: "Open", Model: "test", Scope: config.AgentScopeSystem, Enabled: true},
		{ID: "bind-open-2", Name: "Open Two", Model: "test", Scope: config.AgentScopeSystem, Enabled: true},
		{ID: "bind-private", Name: "Private", Model: "test", Scope: config.AgentScopeRestricted, Enabled: true},
		{ID: "bind-disabled", Name: "Disabled", Model: "test", Scope: config.AgentScopeSystem, Enabled: false},
	} {
		if err := env.store.CreateAgent(ctx, agent); err != nil {
			t.Fatalf("CreateAgent(%s): %v", agent.ID, err)
		}
	}
	if err := env.store.CreateChannel(ctx, config.Channel{
		ID: channelID, Name: "Bind Target", Type: pkgchannel.PlatformTelegram,
		Enabled: enabled, Config: `{"token":"bind-secret"}`,
	}); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	_, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "bind-regular", auth.RoleUser)
	return env, token
}

func storedChannel(t *testing.T, env *testEnv, id string) config.Channel {
	t.Helper()
	ch, err := env.store.GetChannel(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChannel(%s): %v", id, err)
	}
	return ch
}

func TestBindChannelAgentNonAdminBindsAndUnbinds(t *testing.T) {
	env, token := setupChannelBindEnv(t, "bind-tg", true)

	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": "bind-open"})
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var view struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Label     string `json:"label"`
		AgentID   string `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Enabled   bool   `json:"enabled"`
		Config    string `json:"config"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &view); err != nil {
		t.Fatalf("unmarshal bind response: %v", err)
	}
	if view.AgentID != "bind-open" || view.ID != "bind-tg" || !view.Enabled {
		t.Fatalf("bind response = %#v", view)
	}
	if view.AgentName != "Open" || view.Label != "Telegram" {
		t.Fatalf("bind response projection = %#v", view)
	}
	// The public projection must never carry the channel's credentials.
	if view.Config != "" || strings.Contains(rr.Body.String(), "bind-secret") {
		t.Fatalf("bind response leaked channel config: %s", rr.Body.String())
	}

	stored := storedChannel(t, env, "bind-tg")
	if stored.AgentID != "bind-open" {
		t.Fatalf("stored agent_id = %q, want bind-open", stored.AgentID)
	}
	if stored.Config != `{"token":"bind-secret"}` {
		t.Fatalf("bind rewrote channel config: %q", stored.Config)
	}
	if stored.Name != "Bind Target" || !stored.Enabled || stored.Type != pkgchannel.PlatformTelegram {
		t.Fatalf("bind changed non-binding fields: %#v", stored)
	}

	rr = doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": ""})
	if rr.Code != http.StatusOK {
		t.Fatalf("unbind status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if stored := storedChannel(t, env, "bind-tg"); stored.AgentID != "" {
		t.Fatalf("unbind left agent_id = %q", stored.AgentID)
	}
	if stored := storedChannel(t, env, "bind-tg"); stored.Config != `{"token":"bind-secret"}` {
		t.Fatalf("unbind rewrote channel config: %q", stored.Config)
	}
}

func TestBindChannelAgentNonAdminDeniedForInaccessibleAgents(t *testing.T) {
	env, token := setupChannelBindEnv(t, "bind-tg", true)
	ctx := context.Background()

	// Binding an agent the caller cannot use.
	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": "bind-private"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bind private agent = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := storedChannel(t, env, "bind-tg").AgentID; got != "" {
		t.Fatalf("denied bind wrote agent_id = %q", got)
	}

	// Taking a channel away from an agent the caller cannot use.
	ch := storedChannel(t, env, "bind-tg")
	ch.AgentID = "bind-private"
	if err := env.store.UpdateChannel(ctx, ch); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	rr = doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": "bind-open"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("rebind away from private agent = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := storedChannel(t, env, "bind-tg").AgentID; got != "bind-private" {
		t.Fatalf("denied rebind changed agent_id to %q", got)
	}
}

func TestBindChannelAgentNonAdminCannotSeeDisabledChannel(t *testing.T) {
	env, token := setupChannelBindEnv(t, "bind-off", false)

	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/bind-off/bind", map[string]any{"agent_id": "bind-open"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled channel = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := storedChannel(t, env, "bind-off").AgentID; got != "" {
		t.Fatalf("disabled channel bind wrote agent_id = %q", got)
	}
	if rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/missing/bind", map[string]any{"agent_id": "bind-open"}); rr.Code != http.StatusNotFound {
		t.Fatalf("missing channel = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	// An admin may bind a channel that is not enabled yet.
	if rr := doRequest(t, env, http.MethodPost, "/api/channels/bind-off/bind", map[string]any{"agent_id": "bind-open"}); rr.Code != http.StatusOK {
		t.Fatalf("admin bind of disabled channel = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := storedChannel(t, env, "bind-off").AgentID; got != "bind-open" {
		t.Fatalf("admin bind stored agent_id = %q, want bind-open", got)
	}
}

func TestBindChannelAgentEnforcesAgentPlatformUniqueness(t *testing.T) {
	env, token := setupChannelBindEnv(t, "bind-tg", true)
	ctx := context.Background()
	if err := env.store.CreateChannel(ctx, config.Channel{
		ID: "bind-tg-other", Name: "Other", Type: pkgchannel.PlatformTelegram,
		AgentID: "bind-open", Enabled: true, Config: `{"token":"other-secret"}`,
	}); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": "bind-open"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("conflicting bind = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	if want := "agent is already bound to telegram channel bind-tg-other"; parseResponse(t, rr).Error != want {
		t.Fatalf("conflict message = %q, want %q", parseResponse(t, rr).Error, want)
	}
	if got := storedChannel(t, env, "bind-tg").AgentID; got != "" {
		t.Fatalf("conflicting bind wrote agent_id = %q", got)
	}
}

func TestBindChannelAgentRejectsDisabledAndMissingAgents(t *testing.T) {
	env, _ := setupChannelBindEnv(t, "bind-tg", true)

	for _, agentID := range []string{"bind-disabled", "bind-missing"} {
		rr := doRequest(t, env, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": agentID})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("bind %q = %d, want 400 (body: %s)", agentID, rr.Code, rr.Body.String())
		}
	}
	if got := storedChannel(t, env, "bind-tg").AgentID; got != "" {
		t.Fatalf("rejected bind wrote agent_id = %q", got)
	}
}

func TestBindChannelAgentRequiresAuthentication(t *testing.T) {
	env, _ := setupChannelBindEnv(t, "bind-tg", true)
	rr := doUnauthRequest(t, env.srv, http.MethodPost, "/api/channels/bind-tg/bind", map[string]any{"agent_id": "bind-open"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bind = %d, want 401 (body: %s)", rr.Code, rr.Body.String())
	}
}
