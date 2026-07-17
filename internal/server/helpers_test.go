package server

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
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
	"github.com/CherryHQ/stella/internal/credential"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/memory"
	memprofile "github.com/CherryHQ/stella/internal/memory/profile"
	oauthserver "github.com/CherryHQ/stella/internal/oidc"
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
func testServerDeps(t *testing.T, store config.Store, as *appdb.AuthStore, mem memory.Provider, db *pgxpool.Pool, phost *pluginhost.Host) Deps {
	t.Helper()
	const baseURL = "http://localhost:25678"
	oidcStore := appdb.NewOIDCStore(db)
	authSvc := auth.NewAuthService(db, oidcStore, oidcStore, oidcStore)
	sessionMgr, err := auth.NewSessionManager(oidcStore, "test-vault-key")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	recallyStore := recally.NewStore(db)
	credLog := slog.With("component", "admin-test")
	credPATStore := credential.NewPostgresStore(db)
	oauthStore := oauthserver.NewPostgresStore(db)
	credFrontDoor := credential.NewService(credential.Config{PATs: credPATStore, OAuth: oauthStore, Users: credPATStore, Logger: credLog})
	oauthAuthServer := oauthserver.NewService(oauthserver.Config{Store: oauthStore, Issuer: credFrontDoor, Logger: credLog})
	assetHome := t.TempDir()
	assetStore, err := asset.NewStore(assetHome, nil, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	poolMgr := agent.NewPoolManager(store, mem)
	credSvc := connections.NewService(nil, sqlc.New(db), oauth.NewFlowStore(), baseURL)
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
	agentAccess := agentaccess.NewService(store, as)
	sessionSvc, err := sessionaccess.NewService(mem, db, store, assetStore, agentAccess, sessionaccess.WithSystemPromptBuilder(systemPromptBuilder))
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	toolOverrides := agent.NewToolOverrideStore(db)
	agentManagement := agentaccess.NewManagement(agentAccess, store, as, poolMgr, testUserDirectory{users: oidcStore}, agent.NewAgentActivityStore(db), slog.With("component", "agent-management-test"))
	accountSvc := account.NewService(oidcStore, oidcStore, oidcStore, oidcStore, oidcStore, as, credFrontDoor, slog.With("component", "account-test"))
	memProfiles, _ := mem.(memory.ProfileStore)
	memChangelog, _ := mem.(memory.ChangelogReader)
	profileSvc := memprofile.NewService(db, memProfiles, memChangelog, agentAccess, prompt.DefaultAgentSoul, slog.With("component", "profile-test"))
	return Deps{
		DB:                  db,
		Mem:                 mem,
		ChannelResolver:     channel.NewRuntimeResolver(store),
		Account:             accountSvc,
		Profile:             profileSvc,
		AgentAccess:         agentAccess,
		AgentManagement:     agentManagement,
		ToolOverrides:       toolOverrides,
		SessionAccess:       sessionSvc,
		SkillAccess:         skillaccess.NewService(phost.SkillStore(), agentAccess),
		LinkCodes:           auth.NewLinkCodeStore(),
		PoolManager:         poolMgr,
		PluginHost:          phost,
		WeixinRegistrar:     NewTestWeixinRegistrar(),
		BaseURL:             baseURL,
		Credentials:         credSvc,
		ControlPlane:        controlplane.NewService(store, phost, poolMgr, credSvc, slog.With("component", "controlplane-test")),
		Email:               email.NewService(nil, sqlc.New(db)),
		Share:               sharepkg.NewService(sqlc.New(db), mem, recallyStore, assetStore, assetHome, baseURL),
		Recally:             recally.NewService(recallyStore, t.TempDir()),
		CredentialFrontDoor: credFrontDoor,
		OAuthAuthServer:     oauthAuthServer,
		Assets:              assetStore,
		OIDC: OIDCDeps{
			AuthSvc:    authSvc,
			SessionMgr: sessionMgr,
		},
	}
}

// testUserDirectory adapts the OIDC user store to the Agent domain's
// UserDirectory port for tests, mirroring the composition-root adapter.
type testUserDirectory struct {
	users interface {
		GetUser(ctx context.Context, id string) (auth.User, error)
	}
}

func (d testUserDirectory) LookupUser(ctx context.Context, id string) (agentaccess.UserRef, error) {
	u, err := d.users.GetUser(ctx, id)
	if err != nil {
		return agentaccess.UserRef{}, err
	}
	return agentaccess.UserRef{ID: u.ID, Email: u.Email}, nil
}

func (d testUserDirectory) LookupUsers(ctx context.Context, ids []string) ([]agentaccess.UserRef, error) {
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

// newTestServer builds a Server from testServerDeps.
func newTestServer(t *testing.T, store config.Store, as *appdb.AuthStore, mem memory.Provider, db *pgxpool.Pool, phost *pluginhost.Host) *Server {
	t.Helper()
	srv, err := New(context.Background(), testServerDeps(t, store, as, mem, db, phost))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv
}
