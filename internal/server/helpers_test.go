package server

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// testServerDeps builds a full, valid Deps mirroring what the composition root
// assembles — the same shared instances, no shadow construction. Optional
// capabilities are left nil so their endpoints 503.
func testServerDeps(t *testing.T, store config.Store, as *appdb.AuthStore, engine *auth.PolicyEngine, mem memory.Provider, db *pgxpool.Pool, phost *pluginhost.Host) Deps {
	t.Helper()
	const baseURL = "http://localhost:25678"
	oidcStore := appdb.NewOIDCStore(db)
	authSvc := auth.NewAuthService(db, oidcStore, oidcStore, oidcStore)
	sessionMgr, err := auth.NewSessionManager(oidcStore, "test-vault-key")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	recallyStore := recally.NewStore(db)
	credFrontDoor, oauthAuthServer := NewCredentialFrontDoor(db, slog.With("component", "admin-test"))
	assetHome := t.TempDir()
	assetStore, err := asset.NewStore(assetHome, nil, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
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
		SkillStore: pluginhost.NewSkillStoreAdapter(phost.SkillStore()),
		Skills:     skills.BuildPromptSection,
	})
	if err != nil {
		t.Fatalf("sessionaccess.NewSystemPromptBuilder: %v", err)
	}
	sessionSvc, err := sessionaccess.NewService(mem, db, store, as, assetStore, authorizer, sessionaccess.WithSystemPromptBuilder(systemPromptBuilder))
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	return Deps{
		Store:               store,
		DB:                  db,
		AuthStore:           as,
		Mem:                 mem,
		AgentAccess:         agentaccess.NewService(store, as, authorizer),
		SessionAccess:       sessionSvc,
		SkillAccess:         skillaccess.NewService(phost.SkillStore(), agentaccess.NewService(store, as, authorizer), authorizer),
		LinkCodes:           auth.NewLinkCodeStore(),
		PoolManager:         agent.NewPoolManager(store, mem),
		PluginHost:          phost,
		BaseURL:             baseURL,
		Credentials:         connections.NewService(nil, sqlc.New(db), oauth.NewFlowStore(), baseURL),
		Email:               email.NewService(nil, sqlc.New(db), authorizer),
		Share:               sharepkg.NewService(sqlc.New(db), mem, recallyStore, assetStore, assetHome, baseURL),
		Recally:             recally.NewService(recallyStore, t.TempDir()),
		CredentialFrontDoor: credFrontDoor,
		OAuthAuthServer:     oauthAuthServer,
		Assets:              assetStore,
		OIDC: OIDCDeps{
			AuthSvc:     authSvc,
			SessionMgr:  sessionMgr,
			Logins:      oidcStore,
			Users:       oidcStore,
			Sessions:    oidcStore,
			Credentials: oidcStore,
		},
	}
}

// newTestServer builds a Server from testServerDeps.
func newTestServer(t *testing.T, store config.Store, as *appdb.AuthStore, engine *auth.PolicyEngine, mem memory.Provider, db *pgxpool.Pool, phost *pluginhost.Host) *Server {
	t.Helper()
	srv, err := New(context.Background(), testServerDeps(t, store, as, engine, mem, db, phost))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv
}
