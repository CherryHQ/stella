package pluginhost

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/skills"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// TestSkillStoreAdapterDeleteRemovesOnlyUserOwnedRows verifies that the plugin
// boundary requires a trusted actor before permanently deleting mutable data.
func TestSkillStoreAdapterDeleteRemovesOnlyUserOwnedRows(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedSkillAdapterFixtures(t, db)
	raw := skills.New(db)
	store := skills.NewDiskSyncStore(raw, func(scope, agentID string, userID string) string { return "" })
	userSkillID, err := store.Create(ctx, skills.Skill{
		Scope: "user", UserID: userID, Name: "adapter-delete-user", Status: "active",
	}, map[string]string{skills.MainFile: "# user", "reference.md": "keep"})
	if err != nil {
		t.Fatalf("create user skill: %v", err)
	}
	systemSkillID, err := store.Create(ctx, skills.Skill{
		Scope: "system_agent", AgentID: agentID, Name: "adapter-delete-system-agent", Status: "active",
	}, map[string]string{skills.MainFile: "# system"})
	if err != nil {
		t.Fatalf("create system-agent skill: %v", err)
	}
	adapter := NewSkillStoreAdapter(store)

	if err := adapter.Delete(ctx, userSkillID); err == nil {
		t.Fatal("delete without actor succeeded, want fail closed")
	}
	actorCtx := authz.WithUserID(ctx, userID)
	if err := adapter.Delete(actorCtx, systemSkillID); err == nil {
		t.Fatal("system-agent delete succeeded, want fail closed")
	}
	if err := adapter.Delete(actorCtx, userSkillID); err != nil {
		t.Fatalf("delete user skill: %v", err)
	}

	var skillCount, fileCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill WHERE id = $1`, userSkillID).Scan(&skillCount); err != nil {
		t.Fatalf("count deleted skill: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_file WHERE skill_id = $1`, userSkillID).Scan(&fileCount); err != nil {
		t.Fatalf("count deleted skill files: %v", err)
	}
	if skillCount != 0 || fileCount != 0 {
		t.Fatalf("adapter delete retained skill=%d files=%d", skillCount, fileCount)
	}
}

func TestSkillStoreAdapterTouchesReflectSkillUsageThroughDiskSync(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedSkillAdapterFixtures(t, db)
	raw := skills.New(db)
	store := skills.NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		return ""
	})
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-adapter-touch",
		Description:     "created by reflect",
		MainFileContent: "# Reflect Adapter Touch\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	var before int64
	if err := db.QueryRow(ctx, `SELECT use_count FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&before); err != nil {
		t.Fatalf("read initial skill usage: %v", err)
	}
	tracker, ok := NewSkillStoreAdapter(store).(interface {
		TouchReflectSkillRuntimeUse(context.Context, string, string, string) error
	})
	if !ok {
		t.Fatal("skill store adapter does not expose runtime usage touch")
	}
	if err := tracker.TouchReflectSkillRuntimeUse(ctx, created.ID, userID, agentID); err != nil {
		t.Fatalf("TouchReflectSkillRuntimeUse: %v", err)
	}

	var after int64
	if err := db.QueryRow(ctx, `SELECT use_count FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&after); err != nil {
		t.Fatalf("read touched skill usage: %v", err)
	}
	if after != before+1 {
		t.Fatalf("use_count after touch = %d, want %d", after, before+1)
	}
}

func seedSkillAdapterFixtures(t *testing.T, db *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	oidcStore := appdb.NewOIDCStore(db)
	u, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "skill-adapter@test.local",
		Name:  "skill-adapter",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	agentID := "agent1"
	cs := cfgstore.NewDBStore(db)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/" + agentID, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return u.ID, agentID
}
