package authz_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"

	"github.com/CherryHQ/stella/internal/connections"
	credoauth "github.com/CherryHQ/stella/internal/connections/oauth"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	emailpkg "github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestBuiltinToolsDenyForeignResourceAccess(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	ownerUser := uuid.NewString()
	foreignUser := uuid.NewString()
	agentID := uuid.NewString()
	for _, userID := range []string{ownerUser, foreignUser} {
		if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	// System scope so both the owner and the foreign delegated agent (both exact
	// executors of this agent) pass the folded-in agent-read gate; the scheduler
	// job-ownership decision is what differentiates them.
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace, scope) VALUES ($1, 'agent', '/tmp', 'system')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	goalSvc := goal.New(db, q, goal.WithSessionMinter(func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "goal-" + uuid.NewString()
		now := time.Now().UTC()
		_, err := db.Exec(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
			VALUES ($1, $2, 'goal', 'task', 'task', $3, $4, $5, $6, $7)`,
			uuid.NewString(), sessionID, agentID, userID, now, now, now)
		return sessionID, err
	}))
	goalAuthorizer := policy.New(db)
	goalAgents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db), goalAuthorizer)
	goalBundle := goal.NewBundle(q, goalSvc, goalAuthorizer, goalAgents)
	ownerGoalAuthority, err := ownerIdentity(ownerUser, agentID).ToAuthority()
	if err != nil {
		t.Fatalf("owner goal authority: %v", err)
	}
	ownerGoalAcc, err := goalBundle.Begin(ctx, ownerGoalAuthority)
	if err != nil {
		t.Fatalf("goal begin: %v", err)
	}
	ownerGoal, err := ownerGoalAcc.CreateGoal(ctx, goal.CreateInput{AgentID: agentID, Title: "owner goal", Kind: goal.KindComposite})
	if err != nil {
		t.Fatalf("create owner goal: %v", err)
	}

	schedAuthorizer := policy.New(db)
	schedAgents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db), schedAuthorizer)
	schedulerSvc, err := scheduler.New(db, scheduler.WithAuthorization(schedAuthorizer, schedAgents))
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := schedulerSvc.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { _ = schedulerSvc.Stop() })
	ownerAuthority, err := ownerIdentity(ownerUser, agentID).ToAuthority()
	if err != nil {
		t.Fatalf("owner authority: %v", err)
	}
	ownerAcc, err := schedulerSvc.Begin(ctx, ownerAuthority)
	if err != nil {
		t.Fatalf("scheduler begin: %v", err)
	}
	ownerJob, err := ownerAcc.CreateJob(ctx, "owner job", "run", scheduler.Schedule{Every: "1h"}, scheduler.SessionReuse, agentID, "")
	if err != nil {
		t.Fatalf("create owner job: %v", err)
	}

	vaultSvc := newVaultToolTestService(t, db, ownerUser, foreignUser)
	vaultOwnerAuth, err := ownerIdentity(ownerUser, agentID).ToAuthority()
	if err != nil {
		t.Fatalf("vault owner authority: %v", err)
	}
	vaultAcc, err := vaultSvc.Begin(ctx, vaultOwnerAuth)
	if err != nil {
		t.Fatalf("vault begin: %v", err)
	}
	if err := vaultAcc.SetScoped(ctx, vault.ScopeUser, "", "OWNER_USER_SECRET", "secret", vault.SetOptions{}); err != nil {
		t.Fatalf("set owner user vault: %v", err)
	}
	if err := vaultAcc.SetScoped(ctx, vault.ScopeUserAgent, agentID, "OWNER_AGENT_SECRET", "secret", vault.SetOptions{}); err != nil {
		t.Fatalf("set owner agent vault: %v", err)
	}

	foreignCtx := authz.WithAgentID(authz.WithUserID(ctx, foreignUser), agentID)
	goalTool := goal.NewTool(goalBundle)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "get", args: map[string]any{"action": "get", "id": ownerGoal.ID}},
		{name: "cancel", args: map[string]any{"action": "cancel", "id": ownerGoal.ID}},
	} {
		t.Run("goal "+tc.name, func(t *testing.T) {
			if out, err := goalTool.Execute(foreignCtx, tc.args); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
				t.Fatalf("Execute out=%q err=%v, want not-found denial", out, err)
			}
		})
	}
	if out, err := goalTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("goal list foreign err=%v", err)
	} else if strings.Contains(out, ownerGoal.ID) {
		t.Fatalf("goal list leaked owner goal: %s", out)
	}
	if out, err := goalTool.Execute(foreignCtx, map[string]any{"action": "create", "title": "foreign goal"}); err != nil {
		t.Fatalf("goal create foreign own resource err=%v", err)
	} else if strings.Contains(out, ownerGoal.ID) {
		t.Fatalf("goal create leaked owner goal: %s", out)
	}

	schedulerTool := scheduler.NewTool(schedulerSvc)
	for _, action := range []string{"get", "update", "delete", "pause", "resume"} {
		t.Run("scheduler "+action, func(t *testing.T) {
			args := map[string]any{"action": action, "id": ownerJob.ID}
			if action == "update" {
				args["name"] = "new name"
			}
			if out, err := schedulerTool.Execute(foreignCtx, args); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
				t.Fatalf("Execute out=%q err=%v, want forbidden denial", out, err)
			}
		})
	}
	if out, err := schedulerTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("scheduler list foreign err=%v", err)
	} else if strings.Contains(out, ownerJob.ID) {
		t.Fatalf("scheduler list leaked owner job: %s", out)
	}
	out, err := schedulerTool.Execute(foreignCtx, map[string]any{"action": "create", "name": "foreign job", "message": "run", "every": "1h"})
	if err != nil {
		t.Fatalf("scheduler create foreign own resource err=%v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == ownerJob.ID {
		t.Fatalf("scheduler create response=%s err=%v", out, err)
	}

	vaultTool := vault.NewTool(vaultSvc, nil)
	for _, scope := range []string{vault.ScopeSystem, vault.ScopeSystemAgent} {
		for _, action := range []string{"list", "set", "delete"} {
			t.Run("vault "+scope+" "+action, func(t *testing.T) {
				args := map[string]any{"action": action, "scope": scope, "name": "SYS", "value": "secret"}
				if out, err := vaultTool.Execute(foreignCtx, args); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
					t.Fatalf("Execute out=%q err=%v, want system-scope denial", out, err)
				}
			})
		}
	}
	if out, err := vaultTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("vault list foreign err=%v", err)
	} else if strings.Contains(out, "OWNER_USER_SECRET") || strings.Contains(out, "OWNER_AGENT_SECRET") || strings.Contains(out, "secret") {
		t.Fatalf("vault list leaked owner entry or value: %s", out)
	}
	if out, err := vaultTool.Execute(foreignCtx, map[string]any{"action": "delete", "scope": vault.ScopeUserAgent, "name": "OWNER_AGENT_SECRET"}); err != nil {
		t.Fatalf("vault foreign delete own scope err=%v", err)
	} else if strings.Contains(out, "secret") {
		t.Fatalf("vault delete leaked value: %s", out)
	}
	vaultListAcc, err := vaultSvc.Begin(ctx, vaultOwnerAuth)
	if err != nil {
		t.Fatalf("vault begin: %v", err)
	}
	if entries, err := vaultListAcc.ListScoped(ctx, vault.ScopeUserAgent, agentID); err != nil || len(entries) != 1 {
		t.Fatalf("owner agent vault entry affected by foreign delete: entries=%+v err=%v", entries, err)
	}
	if _, err := vaultTool.Execute(context.Background(), map[string]any{"action": "list"}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("vault unauthenticated err=%v, want no user identity", err)
	}

	if err := vaultSvc.SetScoped(ctx, vault.ScopeUser, ownerUser, "", "EMAIL_CONFIG", `{"default":"work","accounts":{"work":{"imap_host":"8.8.8.8","smtp_host":"1.1.1.1","username":"owner@example.com","password":"secret","from":"owner@example.com"}}}`); err != nil {
		t.Fatalf("set owner email config: %v", err)
	}
	emailTool := emailpkg.NewTool(emailpkg.NewService(vaultSvc, q))
	if out, err := emailTool.Execute(foreignCtx, map[string]any{"action": "accounts"}); err == nil || !strings.Contains(err.Error(), "no email account configured") || strings.Contains(out, "work") || strings.Contains(out, "secret") {
		t.Fatalf("email foreign accounts out=%q err=%v, want no leak", out, err)
	}

	flowStore := credoauth.NewFlowStore()
	flowStore.Create(credoauth.FlowStatus{Provider: credoauth.ProviderGitHub, FlowID: "owner-flow", UserID: ownerUser, FlowType: "device_code"})
	oauthSvc := connections.NewService(nil, nil, flowStore, "http://localhost:8080")
	registry := credoauth.NewProviderRegistry()
	registry.Register(credoauth.ProviderConfig{ID: "github", VaultKey: credoauth.VaultKeyGitHub})
	oauthSvc.SetRegistry(registry)
	oauthTool := connections.NewTool(oauthSvc)
	if out, err := oauthTool.Execute(foreignCtx, map[string]any{"action": "status", "provider": "github", "flow_id": "owner-flow"}); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
		t.Fatalf("oauth foreign status out=%q err=%v, want access denied", out, err)
	}
	if out, err := oauthTool.Execute(foreignCtx, map[string]any{"action": "status", "provider": "github", "flow_id": "missing-flow"}); err == nil || !strings.Contains(err.Error(), "flow expired or unknown") || out != "" {
		t.Fatalf("oauth missing status out=%q err=%v, want expired/unknown", out, err)
	}
	if out, err := oauthTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("oauth list err=%v", err)
	} else if strings.Contains(out, "token") || strings.Contains(out, "secret") {
		t.Fatalf("oauth list leaked secret field: %s", out)
	}
	if _, err := oauthTool.Execute(context.Background(), map[string]any{"action": "list"}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("oauth unauthenticated err=%v, want no user identity", err)
	}

	mem := memorytest.New()
	home := t.TempDir()
	ownerSession := "owner-session"
	foreignSession := "foreign-session"
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: ownerSession, UserID: ownerUser, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: foreignSession, UserID: foreignUser, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	root := agent.UserAgentDir(home, foreignUser, agentID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report.html"), []byte("<p>ok</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	shareSvc := sharepkg.NewService(q, mem, recally.NewStore(db), mustAssetStore(t, home), home, "http://stella.test")
	ownerShare, err := q.CreateShare(ctx, sqlc.CreateShareParams{ID: uuid.NewString(), TokenHash: "owner-share-hash", UserID: ownerUser, Title: "owner share", MediaType: "text/html", Content: []byte("owner secret")})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	shareTool := sharepkg.NewTool(shareSvc)
	if out, err := shareTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("share list foreign err=%v", err)
	} else if strings.Contains(out, ownerShare.ID) || strings.Contains(out, "owner secret") {
		t.Fatalf("share list leaked owner share: %s", out)
	}
	if out, err := shareTool.Execute(foreignCtx, map[string]any{"action": "revoke", "id": ownerShare.ID}); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
		t.Fatalf("share foreign revoke out=%q err=%v, want not found", out, err)
	}
	shareCtx := memory.WithSessionID(foreignCtx, foreignSession)
	if out, err := shareTool.Execute(shareCtx, map[string]any{"action": "artifact", "path": "report.html"}); err != nil {
		t.Fatalf("share artifact err=%v", err)
	} else if !strings.Contains(out, "http://stella.test/s/") || strings.Contains(out, "<p>ok</p>") {
		t.Fatalf("share artifact bad response/leaked content: %s", out)
	}
	if _, err := shareTool.Execute(context.Background(), map[string]any{"action": "list"}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("share unauthenticated err=%v, want no user identity", err)
	}

	recallySvc := recally.NewService(recally.NewStore(db), home)
	recallyTool := recally.NewTool(recallySvc)
	recallyOwnerAuth, err := ownerIdentity(ownerUser, agentID).ToAuthority()
	if err != nil {
		t.Fatalf("recally owner authority: %v", err)
	}
	ownerRecallyCtx := authz.WithAgentID(authz.WithUserID(ctx, ownerUser), agentID)
	out, err = recallyTool.Execute(ownerRecallyCtx, map[string]any{"action": "save", "articles": []any{
		map[string]any{"url": "https://example.com/one", "title": "One", "content": "one body"},
		map[string]any{"url": "https://example.com/missing", "title": "Missing"},
		map[string]any{"url": "https://example.com/one?utm_source=x", "canonical_url": "https://example.com/one", "title": "One updated", "content": "updated body"},
	}})
	if err != nil {
		t.Fatalf("recally save err=%v", err)
	}
	if !strings.Contains(out, "created") || !strings.Contains(out, "updated") || !strings.Contains(out, "error") {
		t.Fatalf("recally save did not report partial results: %s", out)
	}
	recallyAcc, err := recallySvc.Access(recallyOwnerAuth)
	if err != nil {
		t.Fatalf("recally begin: %v", err)
	}
	if articles, err := recallyAcc.ListArticles(ctx, recally.ArticleFilter{Limit: 10}); err != nil || len(articles) != 1 {
		t.Fatalf("recally dedup articles=%d err=%v, want one", len(articles), err)
	}
	ownerFeed, err := recallyAcc.CreateFeed(ctx, "https://x.com/cherry", recally.FeedKindTwitter, "Cherry", nil)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if out, err := recallyTool.Execute(foreignCtx, map[string]any{"action": "list_articles"}); err != nil {
		t.Fatalf("recally foreign list_articles err=%v", err)
	} else if strings.Contains(out, "https://example.com/one") {
		t.Fatalf("recally list_articles leaked owner article: %s", out)
	}
	if out, err := recallyTool.Execute(foreignCtx, map[string]any{"action": "get_article", "id": ownerFeed.ID}); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
		t.Fatalf("recally foreign get_article out=%q err=%v, want not found", out, err)
	}
	if out, err := recallyTool.Execute(foreignCtx, map[string]any{"action": "feed_list"}); err != nil {
		t.Fatalf("recally foreign feed_list err=%v", err)
	} else if strings.Contains(out, ownerFeed.ID) {
		t.Fatalf("recally feed_list leaked owner feed: %s", out)
	}
	if out, err := recallyTool.Execute(foreignCtx, map[string]any{"action": "feed_remove", "id": ownerFeed.ID}); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
		t.Fatalf("recally foreign feed_remove out=%q err=%v, want not found", out, err)
	}
	if _, err := recallyTool.Execute(context.Background(), map[string]any{"action": "list_articles"}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("recally unauthenticated err=%v, want no user identity", err)
	}
}

func ownerIdentity(userID, agentID string) authz.Identity {
	return authz.Identity{UserID: userID, AgentID: agentID, AgentScoped: true}
}

func newVaultToolTestService(t *testing.T, db *pgxpool.Pool, userIDs ...string) *vault.Service {
	t.Helper()
	ctx := context.Background()
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	authorizer := policy.New(db)
	agents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db), authorizer)
	svc, err := vault.NewService(sqlc.New(db), masterID.String(), authorizer, agents)
	if err != nil {
		t.Fatalf("vault.NewService: %v", err)
	}
	oidc := appdb.NewOIDCStore(db)
	for _, userID := range userIDs {
		pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
		if err != nil {
			t.Fatalf("GenerateUserKeys: %v", err)
		}
		if err := oidc.UpdateUserAgeKeys(ctx, userID, pubKey, encPrivKey); err != nil {
			t.Fatalf("UpdateUserAgeKeys: %v", err)
		}
	}
	return svc
}

func mustAssetStore(t *testing.T, home string) *asset.Store {
	t.Helper()
	a, err := asset.NewStore(home, nil, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	return a
}
