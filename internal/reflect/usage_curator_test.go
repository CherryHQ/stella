package reflect

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestUsageCuratorShadowReportsWithoutWriting(t *testing.T) {
	store := fakeUsageCuratorStore{
		knowledge: []usageCuratorKnowledgeCandidate{{
			FactID: "fact-1", UserID: "user-1", AgentID: "agent-1",
		}},
		skill: []usageCuratorSkillCandidate{{
			SkillID: "skill-1", UserID: "user-1", AgentID: "agent-1", Version: 3,
		}},
	}
	factWriter := &fakeUsageCuratorFactWriter{}
	skillWriter := &fakeUsageCuratorSkillWriter{}

	report, err := runUsageCurator(context.Background(), usageCuratorRunConfig{
		Store:       store,
		FactWriter:  factWriter,
		SkillWriter: skillWriter,
		Settings: usageCuratorSettings{
			Mode: usageCuratorModeShadow,
			Now:  fixedUsageCuratorNow,
		},
	})
	if err != nil {
		t.Fatalf("runUsageCurator: %v", err)
	}
	if report.KnowledgeCandidates != 1 || report.SkillCandidates != 1 {
		t.Fatalf("report = %#v, want both candidate counts", report)
	}
	if len(factWriter.calls) != 0 {
		t.Fatalf("shadow fact writes = %#v, want none", factWriter.calls)
	}
	if len(skillWriter.inputs) != 0 {
		t.Fatalf("shadow skill writes = %#v, want none", skillWriter.inputs)
	}
}

func TestUsageCuratorArmedDeprecatesKnowledgeAndSkills(t *testing.T) {
	store := fakeUsageCuratorStore{
		knowledge: []usageCuratorKnowledgeCandidate{
			{FactID: "fact-1", UserID: "user-1", AgentID: "agent-1"},
			{FactID: "fact-2", UserID: "user-1", AgentID: "agent-1"},
		},
		skill: []usageCuratorSkillCandidate{{
			SkillID: "skill-1", UserID: "user-1", AgentID: "agent-1", Version: 3, UseCount: 2, Rule: usageCuratorSkillRuleLowUse,
		}},
	}
	factWriter := &fakeUsageCuratorFactWriter{}
	skillWriter := &fakeUsageCuratorSkillWriter{}

	report, err := runUsageCurator(context.Background(), usageCuratorRunConfig{
		Store:       store,
		FactWriter:  factWriter,
		SkillWriter: skillWriter,
		Settings: usageCuratorSettings{
			Mode: usageCuratorModeArmed,
			Now:  fixedUsageCuratorNow,
		},
	})
	if err != nil {
		t.Fatalf("runUsageCurator: %v", err)
	}
	if report.KnowledgeDeprecated != 2 || report.SkillDeprecated != 1 {
		t.Fatalf("report = %#v, want deprecated counts", report)
	}
	if len(factWriter.calls) != 1 {
		t.Fatalf("fact writes = %#v, want one user-agent batch", factWriter.calls)
	}
	call := factWriter.calls[0]
	if call.userID != "user-1" || call.agentID != "agent-1" || len(call.ops) != 1 {
		t.Fatalf("fact call = %#v, want one deprecate_many batch", call)
	}
	op := call.ops[0]
	if op.Action != memorywrite.FactBatchDeprecateMany || op.Subject != memory.FactSubjectWorld {
		t.Fatalf("fact op = %#v, want world deprecate_many", op)
	}
	if !slices.Equal(op.TargetFactIDs, []string{"fact-1", "fact-2"}) {
		t.Fatalf("target facts = %#v, want fact-1/fact-2", op.TargetFactIDs)
	}
	if len(skillWriter.inputs) != 1 {
		t.Fatalf("skill writes = %#v, want one", skillWriter.inputs)
	}
	if got := skillWriter.inputs[0]; got.ID != "skill-1" || got.ExpectedVersion != 3 {
		t.Fatalf("skill deprecate input = %#v, want skill-1 v3", got)
	}
}

func TestSQLUsageCuratorStoreListsOnlyStaleReflectRecordsWithActivity(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	now := fixedUsageCuratorNow()
	lastUsed := now.Add(-45 * 24 * time.Hour)
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, last_active)
		VALUES ($1, 'curator-session-1', 'test', 'chat', $2, $3, $4)
	`, uuid.NewString(), agentID, userID, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	reflectFact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Reflect world fact.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create reflect fact: %v", err)
	}
	manualFact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Manual world fact.",
		Source:  memory.SourceManual,
	})
	if err != nil {
		t.Fatalf("create manual fact: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE knowledge_usage
		SET last_used_at = $1
		WHERE fact_id = $2
	`, lastUsed, reflectFact.ID); err != nil {
		t.Fatalf("seed reflect knowledge usage: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO knowledge_usage (fact_id, user_id, agent_id, last_used_at)
		VALUES ($1, $2, $3, $4)
	`, manualFact.ID, userID, agentID, lastUsed); err != nil {
		t.Fatalf("seed manual knowledge usage: %v", err)
	}

	skillStore := skills.New(db)
	staleSkill, err := skillStore.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "curator-stale-skill", Description: "stale", MainFileContent: "# Stale\n",
	})
	if err != nil {
		t.Fatalf("create stale skill: %v", err)
	}
	highUseSkill, err := skillStore.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "curator-high-use-skill", Description: "high use", MainFileContent: "# High\n",
	})
	if err != nil {
		t.Fatalf("create high-use skill: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE skill_usage
		SET last_used_at = $1, use_count = 2
		WHERE skill_id = $2
	`, now.Add(-25*24*time.Hour), staleSkill.ID); err != nil {
		t.Fatalf("seed stale skill usage: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE skill_usage
		SET last_used_at = $1, use_count = 8
		WHERE skill_id = $2
	`, now.Add(-25*24*time.Hour), highUseSkill.ID); err != nil {
		t.Fatalf("seed high-use skill usage: %v", err)
	}

	curatorStore := NewSQLUsageCuratorStore(q)
	knowledge, err := curatorStore.ListStaleReflectKnowledge(ctx, usageCuratorKnowledgeQuery{
		StaleBefore: now.Add(-20 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListStaleReflectKnowledge: %v", err)
	}
	if len(knowledge) != 1 || knowledge[0].FactID != reflectFact.ID {
		t.Fatalf("knowledge candidates = %#v, want only reflect fact", knowledge)
	}
	skillCandidates, err := curatorStore.ListStaleReflectSkills(ctx, usageCuratorSkillQuery{
		StaleBefore:       now.Add(-60 * 24 * time.Hour),
		LowUseBefore:      now.Add(-20 * 24 * time.Hour),
		LowUseMaxUseCount: 5,
	})
	if err != nil {
		t.Fatalf("ListStaleReflectSkills: %v", err)
	}
	if len(skillCandidates) != 1 || skillCandidates[0].SkillID != staleSkill.ID || skillCandidates[0].Rule != usageCuratorSkillRuleLowUse {
		t.Fatalf("skill candidates = %#v, want only low-use stale skill", skillCandidates)
	}
}

func fixedUsageCuratorNow() time.Time {
	return time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
}

func seedUsageCuratorDB(t *testing.T, ctx context.Context, db *pgxpool.Pool) (string, string) {
	t.Helper()
	user, err := appdb.NewOIDCStore(db).CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "curator@test.local",
		Name:  "curator",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := "curator-agent"
	if err := store.NewDBStore(db).CreateAgent(ctx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/curator-agent", Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return user.ID, agentID
}

type fakeUsageCuratorStore struct {
	knowledge []usageCuratorKnowledgeCandidate
	skill     []usageCuratorSkillCandidate
}

func (s fakeUsageCuratorStore) ListStaleReflectKnowledge(context.Context, usageCuratorKnowledgeQuery) ([]usageCuratorKnowledgeCandidate, error) {
	return append([]usageCuratorKnowledgeCandidate(nil), s.knowledge...), nil
}

func (s fakeUsageCuratorStore) ListStaleReflectSkills(context.Context, usageCuratorSkillQuery) ([]usageCuratorSkillCandidate, error) {
	return append([]usageCuratorSkillCandidate(nil), s.skill...), nil
}

type fakeUsageCuratorFactWriter struct {
	calls []fakeUsageCuratorFactCall
}

type fakeUsageCuratorFactCall struct {
	userID  string
	agentID string
	ops     []memorywrite.FactBatchOperation
}

func (w *fakeUsageCuratorFactWriter) ApplyFactBatch(_ context.Context, userID string, agentID string, ops []memorywrite.FactBatchOperation) ([]memory.Fact, error) {
	w.calls = append(w.calls, fakeUsageCuratorFactCall{
		userID:  userID,
		agentID: agentID,
		ops:     append([]memorywrite.FactBatchOperation(nil), ops...),
	})
	return nil, nil
}

type fakeUsageCuratorSkillWriter struct {
	inputs []skills.ReflectSkillDeprecate
}

func (w *fakeUsageCuratorSkillWriter) DeprecateReflectOwnedUserAgentSkill(_ context.Context, in skills.ReflectSkillDeprecate) (skills.Skill, error) {
	w.inputs = append(w.inputs, in)
	return skills.Skill{ID: in.ID, Status: skills.SkillStatusDeprecated}, nil
}
