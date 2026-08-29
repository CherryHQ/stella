package authz_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/toolmeta"

	"github.com/CherryHQ/stella/internal/connections"
	credoauth "github.com/CherryHQ/stella/internal/connections/oauth"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	emailpkg "github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	homepkg "github.com/CherryHQ/stella/internal/home"
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
	goalAgents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db))
	goalBundle := goal.NewBundle(q, goalSvc, goalAgents)
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

	schedAgents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db))
	schedulerSvc, err := scheduler.New(db, scheduler.WithAgentAccess(schedAgents))
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
	goalTool := func(action string) *goal.Tool {
		return goal.NewTool(goalBundle, actionSpec(t, "goal", goal.ActionTools(), action))
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "get", args: map[string]any{"id": ownerGoal.ID}},
		{name: "cancel", args: map[string]any{"id": ownerGoal.ID}},
	} {
		t.Run("goal "+tc.name, func(t *testing.T) {
			if out, err := goalTool(tc.name).Execute(foreignCtx, tc.args); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
				t.Fatalf("Execute out=%q err=%v, want not-found denial", out, err)
			}
		})
	}
	if out, err := goalTool("list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("goal list foreign err=%v", err)
	} else if strings.Contains(out, ownerGoal.ID) {
		t.Fatalf("goal list leaked owner goal: %s", out)
	}
	if out, err := goalTool("create").Execute(foreignCtx, map[string]any{"title": "foreign goal"}); err != nil {
		t.Fatalf("goal create foreign own resource err=%v", err)
	} else if strings.Contains(out, ownerGoal.ID) {
		t.Fatalf("goal create leaked owner goal: %s", out)
	}
	// A split tool's schema is exact, so an argument that belonged to a sibling
	// action is refused before dispatch instead of being silently dropped.
	if out, err := goalTool("list").Execute(foreignCtx, map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "title") || out != "" {
		t.Fatalf("goal list with a create field out=%q err=%v, want a rejected unknown field", out, err)
	}

	schedulerTool := func(action string) *scheduler.Tool {
		return scheduler.NewTool(schedulerSvc, actionSpec(t, "scheduler", scheduler.ActionTools(), action))
	}
	for _, action := range []string{"get", "update", "delete", "pause", "resume"} {
		t.Run("scheduler "+action, func(t *testing.T) {
			args := map[string]any{"id": ownerJob.ID}
			if action == "update" {
				args["name"] = "new name"
			}
			if out, err := schedulerTool(action).Execute(foreignCtx, args); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
				t.Fatalf("Execute out=%q err=%v, want forbidden denial", out, err)
			}
		})
	}
	if out, err := schedulerTool("list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("scheduler list foreign err=%v", err)
	} else if strings.Contains(out, ownerJob.ID) {
		t.Fatalf("scheduler list leaked owner job: %s", out)
	}
	// pause and resume take the job id only: their one body field is fixed by
	// the service, so a schedule change smuggled into pause is refused.
	if out, err := schedulerTool("pause").Execute(foreignCtx, map[string]any{"id": ownerJob.ID, "every": "1h"}); err == nil || !strings.Contains(err.Error(), "every") || out != "" {
		t.Fatalf("scheduler pause with a body field out=%q err=%v, want a rejected unknown field", out, err)
	}
	out, err := schedulerTool("create").Execute(foreignCtx, map[string]any{"name": "foreign job", "message": "run", "every": "1h"})
	if err != nil {
		t.Fatalf("scheduler create foreign own resource err=%v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == ownerJob.ID {
		t.Fatalf("scheduler create response=%s err=%v", out, err)
	}

	vaultTool := func(action string) *vault.Tool {
		return vault.NewTool(vaultSvc, nil, actionSpec(t, "vault", vault.ActionTools(), action))
	}
	for _, scope := range []string{vault.ScopeSystem, vault.ScopeSystemAgent} {
		for _, action := range []string{"list", "set", "delete"} {
			t.Run("vault "+scope+" "+action, func(t *testing.T) {
				args := map[string]any{"scope": scope, "name": "SYS", "value": "secret"}
				if action == "list" {
					args = map[string]any{"scope": scope}
				}
				if action == "delete" {
					delete(args, "value")
				}
				if out, err := vaultTool(action).Execute(foreignCtx, args); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
					t.Fatalf("Execute out=%q err=%v, want system-scope denial", out, err)
				}
			})
		}
	}
	if out, err := vaultTool("list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("vault list foreign err=%v", err)
	} else if strings.Contains(out, "OWNER_USER_SECRET") || strings.Contains(out, "OWNER_AGENT_SECRET") || strings.Contains(out, "secret") {
		t.Fatalf("vault list leaked owner entry or value: %s", out)
	}
	if out, err := vaultTool("delete").Execute(foreignCtx, map[string]any{"scope": vault.ScopeUserAgent, "name": "OWNER_AGENT_SECRET"}); err != nil {
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
	if _, err := vaultTool("list").Execute(context.Background(), map[string]any{}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("vault unauthenticated err=%v, want no user identity", err)
	}

	if err := vaultSvc.SetScoped(ctx, vault.ScopeUser, ownerUser, "", "EMAIL_CONFIG", `{"default":"work","accounts":{"work":{"imap_host":"8.8.8.8","smtp_host":"1.1.1.1","username":"owner@example.com","password":"secret","from":"owner@example.com"}}}`); err != nil {
		t.Fatalf("set owner email config: %v", err)
	}
	emailSvc := emailpkg.NewService(vaultSvc, q)
	emailTool := func(action string) *emailpkg.Tool {
		return emailpkg.NewTool(emailSvc, actionSpec(t, "email", emailpkg.ActionTools(), action))
	}
	if out, err := emailTool("account_list").Execute(foreignCtx, map[string]any{}); err == nil || !strings.Contains(err.Error(), "no email account configured") || strings.Contains(out, "work") || strings.Contains(out, "secret") {
		t.Fatalf("email foreign accounts out=%q err=%v, want no leak", out, err)
	}
	// The schema maximum now matches the handler's own cap, so an over-limit
	// list is refused with the same number the model was shown.
	if out, err := emailTool("message_list").Execute(foreignCtx, map[string]any{"limit": 500}); err == nil || strings.Contains(out, "work") {
		t.Fatalf("email list over the cap out=%q err=%v, want a refusal", out, err)
	}

	flowStore := credoauth.NewFlowStore()
	flowStore.Create(credoauth.FlowStatus{Provider: credoauth.ProviderGitHub, FlowID: "owner-flow", UserID: ownerUser, FlowType: "device_code"})
	oauthSvc := connections.NewService(nil, nil, flowStore, "http://localhost:8080")
	registry := credoauth.NewProviderRegistry()
	registry.Register(credoauth.ProviderConfig{ID: "github", VaultKey: credoauth.VaultKeyGitHub})
	oauthSvc.SetRegistry(registry)
	oauthTool := func(action string) *connections.Tool {
		return connections.NewTool(oauthSvc, actionSpec(t, "oauth", connections.ActionTools(), action))
	}
	if out, err := oauthTool("flow_status").Execute(foreignCtx, map[string]any{"provider": "github", "flow_id": "owner-flow"}); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
		t.Fatalf("oauth foreign status out=%q err=%v, want access denied", out, err)
	}
	if out, err := oauthTool("flow_status").Execute(foreignCtx, map[string]any{"provider": "github", "flow_id": "missing-flow"}); err == nil || !strings.Contains(err.Error(), "flow expired or unknown") || out != "" {
		t.Fatalf("oauth missing status out=%q err=%v, want expired/unknown", out, err)
	}
	if out, err := oauthTool("list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("oauth list err=%v", err)
	} else if strings.Contains(out, "token") || strings.Contains(out, "secret") {
		t.Fatalf("oauth list leaked secret field: %s", out)
	}
	if _, err := oauthTool("list").Execute(context.Background(), map[string]any{}); err == nil || !strings.Contains(err.Error(), "no user identity") {
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
	shareSvc := sharepkg.NewService(q, mem, recally.NewStore(db), home, "http://stella.test", sharepkg.WithHomeWorkspace(toolAuthzWorkspaceViewer{root: home}), sharepkg.WithAgentAccess(agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db))))
	ownerShare, err := q.CreateShare(ctx, sqlc.CreateShareParams{ID: uuid.NewString(), TokenHash: "owner-share-hash", UserID: ownerUser, Title: "owner share", MediaType: "text/html", Content: []byte("owner secret")})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	shareTool := func(action string) *sharepkg.Tool {
		return sharepkg.NewTool(shareSvc, actionSpec(t, "share", sharepkg.ActionTools(), action))
	}
	if out, err := shareTool("list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("share list foreign err=%v", err)
	} else if strings.Contains(out, ownerShare.ID) || strings.Contains(out, "owner secret") {
		t.Fatalf("share list leaked owner share: %s", out)
	}
	if out, err := shareTool("revoke").Execute(foreignCtx, map[string]any{"id": ownerShare.ID}); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
		t.Fatalf("share foreign revoke out=%q err=%v, want not found", out, err)
	}
	shareCtx := memory.WithSessionID(foreignCtx, foreignSession)
	if out, err := shareTool("artifact_create").Execute(shareCtx, map[string]any{"path": "report.html"}); err != nil {
		t.Fatalf("share artifact err=%v", err)
	} else if !strings.Contains(out, "http://stella.test/s/") || strings.Contains(out, "<p>ok</p>") {
		t.Fatalf("share artifact bad response/leaked content: %s", out)
	}
	// The two create tools require the field each one actually needs, which the
	// union could only describe in prose.
	if out, err := shareTool("artifact_create").Execute(shareCtx, map[string]any{}); err == nil || !strings.Contains(err.Error(), "path") || out != "" {
		t.Fatalf("share_create_artifact without a path out=%q err=%v, want a required-field refusal", out, err)
	}
	if out, err := shareTool("article_create").Execute(shareCtx, map[string]any{}); err == nil || !strings.Contains(err.Error(), "article_id") || out != "" {
		t.Fatalf("share_create_article without an article_id out=%q err=%v, want a required-field refusal", out, err)
	}
	if _, err := shareTool("list").Execute(context.Background(), map[string]any{}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("share unauthenticated err=%v, want no user identity", err)
	}

	recallySvc := recally.NewService(recally.NewStore(db), home)
	recallyTool := func(action string) *recally.Tool {
		return recally.NewTool(recallySvc, actionSpec(t, "recally", recally.ActionTools(), action))
	}
	recallyOwnerAuth, err := ownerIdentity(ownerUser, agentID).ToAuthority()
	if err != nil {
		t.Fatalf("recally owner authority: %v", err)
	}
	ownerRecallyCtx := authz.WithAgentID(authz.WithUserID(ctx, ownerUser), agentID)
	out, err = recallyTool("article_save").Execute(ownerRecallyCtx, map[string]any{"articles": []any{
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
	if out, err := recallyTool("article_list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("recally foreign list_articles err=%v", err)
	} else if strings.Contains(out, "https://example.com/one") {
		t.Fatalf("recally list_articles leaked owner article: %s", out)
	}
	if out, err := recallyTool("article_get").Execute(foreignCtx, map[string]any{"id": ownerFeed.ID}); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
		t.Fatalf("recally foreign get_article out=%q err=%v, want not found", out, err)
	}
	if out, err := recallyTool("feed_list").Execute(foreignCtx, map[string]any{}); err != nil {
		t.Fatalf("recally foreign feed_list err=%v", err)
	} else if strings.Contains(out, ownerFeed.ID) {
		t.Fatalf("recally feed_list leaked owner feed: %s", out)
	}
	if out, err := recallyTool("feed_remove").Execute(foreignCtx, map[string]any{"id": ownerFeed.ID}); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
		t.Fatalf("recally foreign feed_remove out=%q err=%v, want not found", out, err)
	}
	if _, err := recallyTool("article_list").Execute(context.Background(), map[string]any{}); err == nil || !strings.Contains(err.Error(), "no user identity") {
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
	agents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db))
	svc, err := vault.NewService(sqlc.New(db), masterID.String(), agents)
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

// toolAuthzWorkspaceViewer maps this fixture's user/Agent directories;
// production wiring uses the authoritative WorkspaceManager instead.
type toolAuthzWorkspaceViewer struct{ root string }

func (w toolAuthzWorkspaceViewer) WorkspaceView(_ context.Context, req homepkg.WorkspaceRequest) (homepkg.WorkspaceView, error) {
	principal := filepath.Join(w.root, "users", req.UserID)
	if req.GroupID != "" {
		principal = filepath.Join(w.root, "users", "group-"+req.GroupID)
	}
	data, agentRoot := filepath.Join(principal, "data"), filepath.Join(principal, "agents", req.AgentID)
	if err := os.MkdirAll(filepath.Join(data, "assets"), 0o755); err != nil {
		return homepkg.WorkspaceView{}, err
	}
	if err := os.MkdirAll(agentRoot, 0o755); err != nil {
		return homepkg.WorkspaceView{}, err
	}
	return homepkg.WorkspaceView{PrincipalRoot: principal, DataRoot: data, AgentRoot: agentRoot}, nil
}

func (w toolAuthzWorkspaceViewer) OpenRoot(ctx context.Context, req homepkg.WorkspaceRequest, scope homepkg.RootScope, _ homepkg.RootAccess) (homepkg.RootOperations, error) {
	view, err := w.WorkspaceView(ctx, req)
	if err != nil {
		return nil, err
	}
	dir := view.AgentRoot
	if scope == homepkg.RootPrincipalData {
		dir = view.DataRoot
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return toolAuthzRoot{Root: root}, nil
}

type toolAuthzRoot struct{ *os.Root }

func (r toolAuthzRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return r.Root.Stat(name)
}

func (r toolAuthzRoot) List(_ context.Context, _ string, _ homepkg.ListOptions) ([]fs.DirEntry, error) {
	return nil, errors.New("not implemented")
}

func (r toolAuthzRoot) Read(_ context.Context, name string, dst io.Writer, options homepkg.ReadOptions) error {
	file, err := r.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = io.Copy(dst, io.LimitReader(file, options.MaxBytes))
	return err
}

func (r toolAuthzRoot) Write(context.Context, string, io.Reader, homepkg.WriteOptions) error {
	return errors.New("not implemented")
}

func (r toolAuthzRoot) Upload(context.Context, string, io.Reader, homepkg.WriteOptions) error {
	return errors.New("not implemented")
}

func (r toolAuthzRoot) Mkdir(context.Context, string, fs.FileMode, homepkg.MkdirOptions) error {
	return errors.New("not implemented")
}

func (r toolAuthzRoot) Remove(context.Context, string, homepkg.RemoveOptions) error {
	return errors.New("not implemented")
}

func (r toolAuthzRoot) Rename(context.Context, string, string, homepkg.RenameOptions) error {
	return errors.New("not implemented")
}

// actionSpec finds one generated tool by its action key. A split family
// registers one tool per action, so an authorization case names the action it
// exercises instead of passing action= in the arguments.
func actionSpec(t *testing.T, family string, specs []toolmeta.ActionTool, action string) toolmeta.ActionTool {
	t.Helper()
	for _, spec := range specs {
		if spec.Action == action {
			return spec
		}
	}
	t.Fatalf("%s has no action %q", family, action)
	return toolmeta.ActionTool{}
}
