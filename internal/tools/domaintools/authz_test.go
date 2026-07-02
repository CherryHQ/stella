package domaintools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/credentials"
	credoauth "github.com/CherryHQ/stella/internal/credentials/oauth"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/toolctx"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestDomainToolsDenyForeignResourceAccess(t *testing.T) {
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
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'agent', '/tmp')`, agentID); err != nil {
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
	goalBundle := &goal.Service{Queries: q, Goal: goalSvc}
	ownerGoal, err := goalBundle.CreateGoalOwned(ctx, ownerIdentity(ownerUser, agentID), goal.CreateInput{AgentID: agentID, Title: "owner goal", Kind: goal.KindComposite})
	if err != nil {
		t.Fatalf("create owner goal: %v", err)
	}

	schedulerSvc, err := scheduler.New(db)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := schedulerSvc.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { _ = schedulerSvc.Stop() })
	ownerJob, err := schedulerSvc.CreateJobOwned(ctx, ownerIdentity(ownerUser, agentID), "owner job", "run", scheduler.Schedule{Every: "1h"}, scheduler.SessionReuse, agentID)
	if err != nil {
		t.Fatalf("create owner job: %v", err)
	}

	vaultSvc := newVaultToolTestService(t, db, ownerUser, foreignUser)
	if _, err := vaultSvc.SetOwned(ctx, ownerIdentity(ownerUser, agentID), vault.ScopeUser, "OWNER_USER_SECRET", "secret"); err != nil {
		t.Fatalf("set owner user vault: %v", err)
	}
	if _, err := vaultSvc.SetOwned(ctx, ownerIdentity(ownerUser, agentID), vault.ScopeUserAgent, "OWNER_AGENT_SECRET", "secret"); err != nil {
		t.Fatalf("set owner agent vault: %v", err)
	}

	foreignCtx := memory.WithAgentID(memory.WithUserID(ctx, foreignUser), agentID)
	goalTool := NewGoalTool(goalBundle)
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

	schedulerTool := NewSchedulerTool(schedulerSvc)
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
	var created schedulerResponse
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == ownerJob.ID {
		t.Fatalf("scheduler create response=%s err=%v", out, err)
	}

	vaultTool := NewVaultTool(vaultSvc)
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
	if entries, err := vaultSvc.ListOwned(ctx, ownerIdentity(ownerUser, agentID), vault.ScopeUserAgent); err != nil || len(entries) != 1 {
		t.Fatalf("owner agent vault entry affected by foreign delete: entries=%+v err=%v", entries, err)
	}
	if _, err := vaultTool.Execute(context.Background(), map[string]any{"action": "list"}); err == nil || !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("vault unauthenticated err=%v, want no user identity", err)
	}

	flowStore := credoauth.NewFlowStore()
	flowStore.Create(credoauth.FlowStatus{Provider: credoauth.ProviderGitHub, FlowID: "owner-flow", UserID: ownerUser, FlowType: "device_code"})
	oauthSvc := credentials.NewService(nil, nil, flowStore, "http://localhost:8080")
	registry := credoauth.NewProviderRegistry()
	registry.Register(credoauth.ProviderConfig{ID: "github", VaultKey: credoauth.VaultKeyGitHub})
	oauthSvc.SetRegistry(registry)
	oauthTool := NewOauthTool(oauthSvc)
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
}

func ownerIdentity(userID, agentID string) toolctx.Identity {
	return toolctx.Identity{UserID: userID, AgentID: agentID, AgentScoped: true}
}

func newVaultToolTestService(t *testing.T, db *pgxpool.Pool, userIDs ...string) *vault.Service {
	t.Helper()
	ctx := context.Background()
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	svc, err := vault.NewService(sqlc.New(db), masterID.String())
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
