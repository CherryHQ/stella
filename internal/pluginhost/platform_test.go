package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/skills"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

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

func TestSkillStoreAdapterRestoresReflectSkillThroughDiskSync(t *testing.T) {
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
		Name:            "reflect-adapter-restore",
		Description:     "created by reflect",
		MainFileContent: "# Reflect Adapter Restore\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	deprecated, err := store.DeprecateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillDeprecate{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		Metadata:        json.RawMessage(`{"curator":"usage","rule":"adapter_restore","use_count":2,"last_used_at":"2026-06-01T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatalf("DeprecateReflectOwnedUserAgentSkill: %v", err)
	}

	restorer, ok := NewSkillStoreAdapter(store).(interface {
		RestoreReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillRestore) (skills.ReflectSkillRestoreResult, error)
	})
	if !ok {
		t.Fatal("skill store adapter does not expose reflect restore")
	}
	result, err := restorer.RestoreReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillRestore{
		ID:         deprecated.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "test",
		Reason:     "adapter restore test",
	})
	if err != nil {
		t.Fatalf("RestoreReflectOwnedUserAgentSkill: %v", err)
	}
	if !result.Restored {
		t.Fatal("RestoreReflectOwnedUserAgentSkill restored = false, want true")
	}
	if result.Skill.Status != "active" {
		t.Fatalf("restored skill status = %q, want active", result.Skill.Status)
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
