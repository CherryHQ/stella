package reflect

import (
	"context"
	"encoding/json"
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
	"github.com/CherryHQ/stella/internal/memory/lcm"
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
	if len(factWriter.calls) != 2 {
		t.Fatalf("fact writes = %#v, want one batch per candidate", factWriter.calls)
	}
	call := factWriter.calls[0]
	if call.userID != "user-1" || call.agentID != "agent-1" || len(call.ops) != 1 {
		t.Fatalf("fact call = %#v, want one deprecate_many batch", call)
	}
	op := call.ops[0]
	if op.Action != memorywrite.FactBatchDeprecateMany || op.Subject != memory.FactSubjectWorld {
		t.Fatalf("fact op = %#v, want world deprecate_many", op)
	}
	if !slices.Equal(op.TargetFactIDs, []string{"fact-1"}) {
		t.Fatalf("target facts = %#v, want fact-1", op.TargetFactIDs)
	}
	if len(skillWriter.inputs) != 1 {
		t.Fatalf("skill writes = %#v, want one", skillWriter.inputs)
	}
	if got := skillWriter.inputs[0]; got.ID != "skill-1" || got.ExpectedVersion != 3 {
		t.Fatalf("skill deprecate input = %#v, want skill-1 v3", got)
	}
}

func TestUsageCuratorArmedSkipsKnowledgeWhenUsageChangedAfterSelection(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	provider, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new lcm provider: %v", err)
	}
	fact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Reflect world fact.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create reflect fact: %v", err)
	}
	selectedLastUsed := fixedUsageCuratorNow().Add(-30 * 24 * time.Hour)
	if _, err := db.Exec(ctx, `
		UPDATE knowledge_usage
		SET last_used_at = $1
		WHERE fact_id = $2
	`, selectedLastUsed, fact.ID); err != nil {
		t.Fatalf("seed stale knowledge usage: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE knowledge_usage
		SET last_used_at = $1
		WHERE fact_id = $2
	`, fixedUsageCuratorNow(), fact.ID); err != nil {
		t.Fatalf("simulate knowledge usage after selection: %v", err)
	}

	deprecated, err := deprecateCuratorKnowledge(ctx, provider, []usageCuratorKnowledgeCandidate{{
		FactID:     fact.ID,
		UserID:     userID,
		AgentID:    agentID,
		LastUsedAt: selectedLastUsed,
	}})
	if err != nil {
		t.Fatalf("deprecateCuratorKnowledge: %v", err)
	}
	if deprecated != 0 {
		t.Fatalf("deprecated = %d, want 0", deprecated)
	}
	row, err := q.GetFact(ctx, sqlc.GetFactParams{ID: fact.ID, UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("read fact after curator: %v", err)
	}
	if row.Status != string(memory.FactStatusActive) {
		t.Fatalf("fact status = %s, want active", row.Status)
	}
}

func TestUsageCuratorArmedDoesNotRollBackSiblingKnowledgeWhenOneCandidateChanged(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	provider, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new lcm provider: %v", err)
	}
	selectedLastUsed := fixedUsageCuratorNow().Add(-30 * 24 * time.Hour)
	changedFact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Changed Reflect fact.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create changed fact: %v", err)
	}
	staleFact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Still stale Reflect fact.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create stale fact: %v", err)
	}
	for _, factID := range []string{changedFact.ID, staleFact.ID} {
		if _, err := db.Exec(ctx, `
			UPDATE knowledge_usage
			SET last_used_at = $1
			WHERE fact_id = $2
		`, selectedLastUsed, factID); err != nil {
			t.Fatalf("seed stale knowledge usage: %v", err)
		}
	}
	if _, err := db.Exec(ctx, `
		UPDATE knowledge_usage
		SET last_used_at = $1
		WHERE fact_id = $2
	`, fixedUsageCuratorNow(), changedFact.ID); err != nil {
		t.Fatalf("simulate changed knowledge usage: %v", err)
	}

	deprecated, err := deprecateCuratorKnowledge(ctx, provider, []usageCuratorKnowledgeCandidate{
		{FactID: changedFact.ID, UserID: userID, AgentID: agentID, LastUsedAt: selectedLastUsed},
		{FactID: staleFact.ID, UserID: userID, AgentID: agentID, LastUsedAt: selectedLastUsed},
	})
	if err != nil {
		t.Fatalf("deprecateCuratorKnowledge: %v", err)
	}
	if deprecated != 1 {
		t.Fatalf("deprecated = %d, want 1", deprecated)
	}
	changedRow, err := q.GetFact(ctx, sqlc.GetFactParams{ID: changedFact.ID, UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("read changed fact: %v", err)
	}
	staleRow, err := q.GetFact(ctx, sqlc.GetFactParams{ID: staleFact.ID, UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("read stale fact: %v", err)
	}
	if changedRow.Status != string(memory.FactStatusActive) {
		t.Fatalf("changed fact status = %s, want active", changedRow.Status)
	}
	if staleRow.Status != string(memory.FactStatusDeprecated) {
		t.Fatalf("stale fact status = %s, want deprecated", staleRow.Status)
	}
}

func TestUsageCuratorArmedRecordsKnowledgeDeprecateMetadata(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	provider, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new lcm provider: %v", err)
	}
	fact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Stale Reflect knowledge.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create reflect fact: %v", err)
	}
	lastUsed := fixedUsageCuratorNow().Add(-30 * 24 * time.Hour)
	pairLatestActivity := fixedUsageCuratorNow().Add(-24 * time.Hour)
	if _, err := db.Exec(ctx, `
		UPDATE knowledge_usage
		SET last_used_at = $1
		WHERE fact_id = $2
	`, lastUsed, fact.ID); err != nil {
		t.Fatalf("seed stale knowledge usage: %v", err)
	}

	deprecated, err := deprecateCuratorKnowledge(ctx, provider, []usageCuratorKnowledgeCandidate{{
		FactID:               fact.ID,
		UserID:               userID,
		AgentID:              agentID,
		LastUsedAt:           lastUsed,
		PairLatestActivityAt: pairLatestActivity,
	}})
	if err != nil {
		t.Fatalf("deprecateCuratorKnowledge: %v", err)
	}
	if deprecated != 1 {
		t.Fatalf("deprecated = %d, want 1", deprecated)
	}

	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "fact",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list memory changelog: %v", err)
	}
	if len(logs) == 0 || logs[0].Action != "deprecate" || logs[0].EntityID.String != fact.ID {
		t.Fatalf("latest fact changelog = %#v, want deprecate for %s", logs, fact.ID)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(logs[0].Metadata.String), &metadata); err != nil {
		t.Fatalf("unmarshal deprecate metadata: %v", err)
	}
	if metadata["curator"] != "usage" || metadata["rule"] != "idle" {
		t.Fatalf("metadata = %#v, want curator usage idle", metadata)
	}
	if metadata["last_used_at"] == "" || metadata["pair_latest_activity_at"] == "" {
		t.Fatalf("metadata missing usage timestamps: %#v", metadata)
	}
}

func TestUsageCuratorArmedSkipsSkillWhenUsageChangedAfterSelection(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	skillStore := skills.New(db)
	created, err := skillStore.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "curator-skill-used-after-selection",
		Description:     "selected then used",
		MainFileContent: "# Selected Then Used\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	selectedLastUsed := fixedUsageCuratorNow().Add(-30 * 24 * time.Hour)
	if _, err := db.Exec(ctx, `
		UPDATE skill_usage
		SET last_used_at = $1, use_count = 2
		WHERE skill_id = $2
	`, selectedLastUsed, created.ID); err != nil {
		t.Fatalf("seed stale skill usage: %v", err)
	}
	if err := skillStore.TouchReflectSkillRuntimeUse(ctx, created.ID, userID, agentID); err != nil {
		t.Fatalf("TouchReflectSkillRuntimeUse: %v", err)
	}

	deprecated, err := deprecateCuratorSkills(ctx, skillStore, []usageCuratorSkillCandidate{{
		SkillID:    created.ID,
		UserID:     userID,
		AgentID:    agentID,
		Version:    created.Version,
		UseCount:   2,
		LastUsedAt: selectedLastUsed,
		Rule:       usageCuratorSkillRuleLowUse,
	}})
	if err != nil {
		t.Fatalf("deprecateCuratorSkills: %v", err)
	}
	if deprecated != 0 {
		t.Fatalf("deprecated = %d, want 0", deprecated)
	}
	skill, err := skillStore.Resolve(ctx, created.Name, skills.ViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("resolve skill after curator: %v", err)
	}
	if skill == nil || skill.Status != skills.SkillStatusActive {
		t.Fatalf("skill after curator = %#v, want active", skill)
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

func TestSQLRecentlyForgottenStoreListsRestorableKnowledgeAndSkillCandidates(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)

	fact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Forgotten knowledge content.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create reflect fact: %v", err)
	}
	factMetadata := json.RawMessage(`{"curator":"usage","rule":"idle","last_used_at":"2026-06-01T00:00:00Z"}`)
	if _, err := memorywrite.ApplyFactBatch(ctx, db, q, userID, agentID, []memorywrite.FactBatchOperation{{
		Action:        memorywrite.FactBatchDeprecateMany,
		Subject:       memory.FactSubjectWorld,
		TargetFactIDs: []string{fact.ID},
		Metadata:      factMetadata,
	}}); err != nil {
		t.Fatalf("deprecate reflect fact: %v", err)
	}

	skillStore := skills.New(db)
	skill, err := skillStore.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "forgotten-skill",
		Description:     "forgotten skill description",
		MainFileContent: "# Forgotten Skill\n\nFull body should not appear in list.\n",
	})
	if err != nil {
		t.Fatalf("create reflect skill: %v", err)
	}
	skillMetadata := json.RawMessage(`{"curator":"usage","rule":"low_use","use_count":4,"last_used_at":"2026-06-01T00:00:00Z"}`)
	if _, err := skillStore.DeprecateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillDeprecate{
		ID:              skill.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: skill.Version,
		Metadata:        skillMetadata,
	}); err != nil {
		t.Fatalf("deprecate reflect skill: %v", err)
	}

	store := NewSQLRecentlyForgottenStore(q)
	items, err := store.ListRecentlyForgotten(ctx, RecentlyForgottenQuery{
		UserID:  userID,
		AgentID: agentID,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListRecentlyForgotten: %v", err)
	}
	if len(items.Knowledge) != 1 || items.Knowledge[0].FactID != fact.ID || items.Knowledge[0].Content != fact.Content {
		t.Fatalf("knowledge items = %#v, want fact content", items.Knowledge)
	}
	if len(items.Skills) != 1 || items.Skills[0].SkillID != skill.ID || items.Skills[0].Name != skill.Name {
		t.Fatalf("skill items = %#v, want skill catalog", items.Skills)
	}
	if items.Skills[0].MainFileContent != "" {
		t.Fatalf("skill list leaked main file content %q", items.Skills[0].MainFileContent)
	}
}

func TestSQLForgottenRestoreServiceRestoresKnowledgeAndSkill(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)

	fact, err := memorywrite.CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Restore service knowledge.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create reflect fact: %v", err)
	}
	if _, err := memorywrite.ApplyFactBatch(ctx, db, q, userID, agentID, []memorywrite.FactBatchOperation{{
		Action:        memorywrite.FactBatchDeprecateMany,
		Subject:       memory.FactSubjectWorld,
		TargetFactIDs: []string{fact.ID},
		Metadata:      json.RawMessage(`{"curator":"usage","rule":"idle","last_used_at":"2026-06-01T00:00:00Z"}`),
	}}); err != nil {
		t.Fatalf("deprecate reflect fact: %v", err)
	}

	skillStore := skills.New(db)
	skill, err := skillStore.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "restore-service-skill",
		Description:     "restore service skill",
		MainFileContent: "# Restore Service Skill\n",
	})
	if err != nil {
		t.Fatalf("create reflect skill: %v", err)
	}
	if _, err := skillStore.DeprecateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillDeprecate{
		ID:              skill.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: skill.Version,
		Metadata:        json.RawMessage(`{"curator":"usage","rule":"unused","use_count":3,"last_used_at":"2026-06-01T00:00:00Z"}`),
	}); err != nil {
		t.Fatalf("deprecate reflect skill: %v", err)
	}

	service := NewSQLForgottenRestoreService(db, q, skillStore)
	knowledgeResult, err := service.RestoreForgotten(ctx, RestoreForgottenRequest{
		Kind:       RecentlyForgottenKindKnowledge,
		ID:         fact.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "admin@example.com",
		Reason:     "false positive",
	})
	if err != nil {
		t.Fatalf("RestoreForgotten knowledge: %v", err)
	}
	if !knowledgeResult.Restored || knowledgeResult.Knowledge == nil || knowledgeResult.Knowledge.Status != memory.FactStatusActive {
		t.Fatalf("knowledge restore result = %#v, want active restored fact", knowledgeResult)
	}

	skillResult, err := service.RestoreForgotten(ctx, RestoreForgottenRequest{
		Kind:       RecentlyForgottenKindSkill,
		ID:         skill.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "admin@example.com",
	})
	if err != nil {
		t.Fatalf("RestoreForgotten skill: %v", err)
	}
	if !skillResult.Restored || skillResult.Skill == nil || skillResult.Skill.Status != skills.SkillStatusActive {
		t.Fatalf("skill restore result = %#v, want active restored skill", skillResult)
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
	facts := make([]memory.Fact, 0, len(ops))
	for _, op := range ops {
		for _, id := range op.TargetFactIDs {
			facts = append(facts, memory.Fact{ID: id, Status: memory.FactStatusDeprecated})
		}
	}
	return facts, nil
}

type fakeUsageCuratorSkillWriter struct {
	inputs []skills.ReflectSkillDeprecate
}

func (w *fakeUsageCuratorSkillWriter) DeprecateReflectOwnedUserAgentSkill(_ context.Context, in skills.ReflectSkillDeprecate) (skills.Skill, error) {
	w.inputs = append(w.inputs, in)
	return skills.Skill{ID: in.ID, Status: skills.SkillStatusDeprecated}, nil
}
