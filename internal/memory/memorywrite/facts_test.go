package memorywrite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestCreateFact_BumpsMemoryVersionAndWritesChangelog(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	fact, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectUser,
		Content: "Prefers concise answers.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact: %v", err)
	}
	if fact.Version != 1 {
		t.Fatalf("fact version = %d, want 1", fact.Version)
	}
	if fact.Status != memory.FactStatusActive {
		t.Fatalf("fact status = %q, want active", fact.Status)
	}

	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("GetUserAgentMemory: %v", err)
	}
	if row.Version != 1 {
		t.Fatalf("memory version = %d, want 1", row.Version)
	}

	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "fact",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMemoryChangelog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("changelog entries = %d, want 1", len(logs))
	}
	if logs[0].EntityID.String != fact.ID {
		t.Fatalf("changelog entity_id = %q, want %q", logs[0].EntityID.String, fact.ID)
	}
	if logs[0].MemoryVersionAfter.Int64 != 1 {
		t.Fatalf("changelog memory_version_after = %d, want 1", logs[0].MemoryVersionAfter.Int64)
	}
	if !logs[0].AfterText.Valid {
		t.Fatalf("changelog after_text is invalid")
	}
	var state memory.Fact
	if err := json.Unmarshal([]byte(logs[0].AfterText.String), &state); err != nil {
		t.Fatalf("unmarshal changelog after_text: %v", err)
	}
	if state.ID != fact.ID || state.Content != fact.Content {
		t.Fatalf("changelog state = %+v, want fact %+v", state, fact)
	}
}

func TestCreateFact_ProfileAndSoulAreSingletonsKnowledgeIsCollection(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "First profile.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("create first profile fact: %v", err)
	}
	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Second profile.", Source: memory.SourceReflect,
	}); err == nil {
		t.Fatalf("expected second active profile fact to fail singleton constraint")
	}

	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectAgent, Content: "First soul.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("create first soul fact: %v", err)
	}
	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectAgent, Content: "Second soul.", Source: memory.SourceReflect,
	}); err == nil {
		t.Fatalf("expected second active soul fact to fail singleton constraint")
	}

	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "World fact A.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("create first knowledge fact: %v", err)
	}
	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "World fact B.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("create second knowledge fact: %v", err)
	}
}

func TestReplaceFact_DeprecatesOldFactAndSupersedesIt(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	oldFact, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Prefers verbose answers.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create old fact: %v", err)
	}

	newFact, err := ReplaceFact(ctx, db, q, oldFact.ID, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Prefers concise answers.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("ReplaceFact: %v", err)
	}
	if newFact.Supersedes != oldFact.ID {
		t.Fatalf("new fact supersedes = %q, want %q", newFact.Supersedes, oldFact.ID)
	}
	if newFact.Version != 1 {
		t.Fatalf("new fact version = %d, want 1", newFact.Version)
	}

	active, err := ListActiveFacts(ctx, q, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		t.Fatalf("ListActiveFacts: %v", err)
	}
	if len(active) != 1 || active[0].ID != newFact.ID {
		t.Fatalf("active profile facts = %+v, want only %s", active, newFact.ID)
	}

	oldAfter, err := q.GetFact(ctx, sqlc.GetFactParams{ID: oldFact.ID, UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("GetFact old: %v", err)
	}
	if oldAfter.Status != string(memory.FactStatusDeprecated) {
		t.Fatalf("old status = %q, want deprecated", oldAfter.Status)
	}
	if oldAfter.Version != 2 {
		t.Fatalf("old version = %d, want 2", oldAfter.Version)
	}
}

func TestApplyFactBatch_ReflectWorldFactsMaintainUsageRows(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	written, err := ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{{
		Action:  FactBatchCreate,
		Subject: memory.FactSubjectWorld,
		Content: "The repo uses OpenAPI as the API source of truth.",
	}})
	if err != nil {
		t.Fatalf("ApplyFactBatch create: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("written facts = %d, want 1", len(written))
	}

	var createdLastUsed time.Time
	if err := db.QueryRow(ctx, `
		SELECT last_used_at
		FROM knowledge_usage
		WHERE fact_id = $1 AND user_id = $2 AND agent_id = $3
	`, written[0].ID, userID, agentID).Scan(&createdLastUsed); err != nil {
		t.Fatalf("read created knowledge usage: %v", err)
	}
	if createdLastUsed.IsZero() {
		t.Fatal("created knowledge usage last_used_at is zero")
	}

	replacement, err := ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{{
		Action:        FactBatchReplaceMany,
		Subject:       memory.FactSubjectWorld,
		TargetFactIDs: []string{written[0].ID},
		Content:       "The repo uses OpenAPI specs and generated server/client code for API changes.",
	}})
	if err != nil {
		t.Fatalf("ApplyFactBatch replace: %v", err)
	}
	if len(replacement) != 1 {
		t.Fatalf("replacement facts = %d, want 1", len(replacement))
	}

	var oldUsageCount int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM knowledge_usage
		WHERE fact_id = $1
	`, written[0].ID).Scan(&oldUsageCount); err != nil {
		t.Fatalf("count old knowledge usage: %v", err)
	}
	if oldUsageCount != 0 {
		t.Fatalf("old knowledge usage rows = %d, want 0", oldUsageCount)
	}

	var newUsageCount int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM knowledge_usage
		WHERE fact_id = $1 AND user_id = $2 AND agent_id = $3
	`, replacement[0].ID, userID, agentID).Scan(&newUsageCount); err != nil {
		t.Fatalf("count replacement knowledge usage: %v", err)
	}
	if newUsageCount != 1 {
		t.Fatalf("replacement knowledge usage rows = %d, want 1", newUsageCount)
	}
}

func TestListActiveFactsAt_UsesMemoryVersionSnapshot(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	oldFact, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Old profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create old fact: %v", err)
	}

	snapVersion := int64(1)
	newFact, err := ReplaceFact(ctx, db, q, oldFact.ID, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "New profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("ReplaceFact: %v", err)
	}

	atSnapshot, err := ListActiveFactsAt(ctx, q, userID, agentID, memory.FactSubjectUser, snapVersion)
	if err != nil {
		t.Fatalf("ListActiveFactsAt snapshot: %v", err)
	}
	if len(atSnapshot) != 1 || atSnapshot[0].ID != oldFact.ID {
		t.Fatalf("snapshot facts = %+v, want old fact %s", atSnapshot, oldFact.ID)
	}

	current, err := ListActiveFacts(ctx, q, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		t.Fatalf("ListActiveFacts current: %v", err)
	}
	if len(current) != 1 || current[0].ID != newFact.ID {
		t.Fatalf("current facts = %+v, want new fact %s", current, newFact.ID)
	}
}

func TestListActiveFactsAt_VersionZeroStaysEmptyAfterLaterWrites(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)
	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "Later world fact.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("create fact: %v", err)
	}

	atVersionZero, err := ListActiveFactsAt(ctx, q, userID, agentID, memory.FactSubjectWorld, 0)
	if err != nil {
		t.Fatalf("ListActiveFactsAt version 0: %v", err)
	}
	if len(atVersionZero) != 0 {
		t.Fatalf("facts at version 0 = %+v, want empty", atVersionZero)
	}
}

func TestSetSingletonFact_DecidesCreateOrReplaceUnderLock(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	first, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "First profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("SetSingletonFact first: %v", err)
	}
	second, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Second profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("SetSingletonFact second: %v", err)
	}
	if second.Supersedes != first.ID {
		t.Fatalf("second supersedes = %q, want %q", second.Supersedes, first.ID)
	}

	active, err := ListActiveFacts(ctx, q, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		t.Fatalf("ListActiveFacts: %v", err)
	}
	if len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("active profile facts = %+v, want only second fact", active)
	}
}

func TestSetSingletonFact_NoopsWhenContentUnchanged(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	first, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Same profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("SetSingletonFact first: %v", err)
	}
	before, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("get memory before no-op: %v", err)
	}
	beforeLogs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "fact",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list changelog before no-op: %v", err)
	}

	second, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Same profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("SetSingletonFact same content: %v", err)
	}
	after, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("get memory after no-op: %v", err)
	}
	afterLogs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "fact",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list changelog after no-op: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("same content returned fact %s, want existing fact %s", second.ID, first.ID)
	}
	if after.Version != before.Version {
		t.Fatalf("same content bumped memory version from %d to %d", before.Version, after.Version)
	}
	if len(afterLogs) != len(beforeLogs) {
		t.Fatalf("same content wrote %d changelog entries, want unchanged %d", len(afterLogs), len(beforeLogs))
	}
}

func TestApplyFactBatch_RollsBackWhenLaterOperationFails(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)
	oldWorld, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Old world fact.",
		Source:  memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact old world: %v", err)
	}

	_, err = ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{
		{
			Action:  FactBatchSetSingleton,
			Subject: memory.FactSubjectUser,
			Content: "New profile that should roll back.",
		},
		{
			Action:        FactBatchReplaceMany,
			Subject:       memory.FactSubjectWorld,
			TargetFactIDs: []string{"missing-fact"},
			Content:       "Replacement that cannot be applied.",
		},
	})
	if err == nil {
		t.Fatal("expected batch failure")
	}

	profiles, err := ListActiveFacts(ctx, q, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		t.Fatalf("ListActiveFacts profile: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profile write should have rolled back, got %#v", profiles)
	}
	worlds, err := ListActiveFacts(ctx, q, userID, agentID, memory.FactSubjectWorld)
	if err != nil {
		t.Fatalf("ListActiveFacts world: %v", err)
	}
	if len(worlds) != 1 || worlds[0].ID != oldWorld.ID {
		t.Fatalf("old world fact should remain active after rollback, got %#v", worlds)
	}
}

func TestApplyFactBatch_ReplacesManyWorldFacts(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)
	first, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "Old world fact A.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact first: %v", err)
	}
	second, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "Old world fact B.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact second: %v", err)
	}

	entityMetadata := json.RawMessage(`{"entity_label":"consolidated"}`)
	changelogMetadata := json.RawMessage(`{"reflect_provenance":{"run_id":"run-1","operation_ref":"knowledge-0001"}}`)
	written, err := ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{{
		Action:            FactBatchReplaceMany,
		Subject:           memory.FactSubjectWorld,
		TargetFactIDs:     []string{first.ID, second.ID},
		Content:           "New consolidated world fact.",
		Metadata:          entityMetadata,
		ChangelogMetadata: changelogMetadata,
	}})
	if err != nil {
		t.Fatalf("ApplyFactBatch: %v", err)
	}
	if len(written) != 1 || written[0].Content != "New consolidated world fact." {
		t.Fatalf("unexpected written facts: %#v", written)
	}

	active, err := ListActiveFacts(ctx, q, userID, agentID, memory.FactSubjectWorld)
	if err != nil {
		t.Fatalf("ListActiveFacts world: %v", err)
	}
	if len(active) != 1 || active[0].ID != written[0].ID {
		t.Fatalf("expected only replacement active, got %#v", active)
	}
	if active[0].Source != memory.SourceReflect {
		t.Fatalf("replacement source = %q, want reflect", active[0].Source)
	}
	var entityFields map[string]any
	if err := json.Unmarshal(active[0].Metadata, &entityFields); err != nil {
		t.Fatalf("unmarshal replacement entity metadata: %v", err)
	}
	if entityFields["entity_label"] != "consolidated" {
		t.Fatalf("replacement entity metadata = %#v", entityFields)
	}
	if _, exists := entityFields["reflect_provenance"]; exists {
		t.Fatalf("changelog provenance leaked into fact entity metadata: %#v", entityFields)
	}

	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID: userID, AgentID: agentID, Scope: factsScope, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list replacement changelog: %v", err)
	}
	matchingLogs := 0
	for _, log := range logs {
		if log.Metadata.Valid && log.Metadata.String == string(changelogMetadata) {
			matchingLogs++
		}
	}
	if matchingLogs != 3 {
		t.Fatalf("changelog rows sharing operation provenance = %d, want 3", matchingLogs)
	}
}

func TestApplyFactBatch_SingletonReplaceSharesChangelogMetadata(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)
	if _, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser,
		Content: "Old profile.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("seed old profile: %v", err)
	}
	changelogMetadata := json.RawMessage(`{"reflect_provenance":{"run_id":"run-1","operation_ref":"profile"}}`)
	if _, err := ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{{
		Action: FactBatchSetSingleton, Subject: memory.FactSubjectUser,
		Content: "Old profile plus a durable preference.", ChangelogMetadata: changelogMetadata,
	}}); err != nil {
		t.Fatalf("replace profile: %v", err)
	}

	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID: userID, AgentID: agentID, Scope: factsScope, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list profile changelog: %v", err)
	}
	matchingActions := map[string]int{}
	var sharedVersion int64
	for _, log := range logs {
		if !log.Metadata.Valid || log.Metadata.String != string(changelogMetadata) {
			continue
		}
		matchingActions[log.Action]++
		if sharedVersion == 0 {
			sharedVersion = log.MemoryVersionAfter.Int64
		} else if log.MemoryVersionAfter.Int64 != sharedVersion {
			t.Fatalf("singleton replacement changelog versions differ: %#v", logs)
		}
	}
	if matchingActions["deprecate"] != 1 || matchingActions["replace"] != 1 {
		t.Fatalf("singleton replacement provenance actions = %#v", matchingActions)
	}
}

func TestApplyFactBatch_RejectsInvalidChangelogMetadataBeforeWriting(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := ApplyFactBatch(context.Background(), db, q, userID, agentID, []FactBatchOperation{
		{
			Action:  FactBatchSetSingleton,
			Subject: memory.FactSubjectUser,
			Content: "This write must not be committed.",
		},
		{
			Action:            FactBatchCreate,
			Subject:           memory.FactSubjectWorld,
			Content:           "Invalid provenance must fail the whole batch.",
			ChangelogMetadata: json.RawMessage(`[]`),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changelog metadata must be a JSON object") {
		t.Fatalf("expected changelog metadata validation error, got %v", err)
	}

	profiles, err := ListActiveFacts(context.Background(), q, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		t.Fatalf("list profile facts: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("invalid metadata committed an earlier batch operation: %#v", profiles)
	}
}

func TestResetUserAgentMemory_KeepsVersionMonotonicAndDoesNotResurrect(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	if _, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Old profile.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("set old profile: %v", err)
	}
	worldFacts, err := ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{{
		Action:  FactBatchCreate,
		Subject: memory.FactSubjectWorld,
		Content: "Old world knowledge.",
	}})
	if err != nil {
		t.Fatalf("create old world fact: %v", err)
	}
	if _, err := AddConstraint(ctx, db, q, userID, agentID, "Old constraint."); err != nil {
		t.Fatalf("add old constraint: %v", err)
	}
	before, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("get memory before reset: %v", err)
	}

	if err := ResetUserAgentMemory(ctx, db, q, userID, agentID); err != nil {
		t.Fatalf("ResetUserAgentMemory: %v", err)
	}

	// The memory row must survive the reset so the version clock never restarts
	// at 1; deleting it would let frozen sessions replay stale fact state.
	afterReset, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("memory row should survive reset: %v", err)
	}
	if afterReset.Version <= before.Version {
		t.Fatalf("reset version = %d, want > %d (monotonic)", afterReset.Version, before.Version)
	}
	if string(afterReset.Constraints) != "[]" || string(afterReset.ProfileEntries) != "[]" {
		t.Fatalf("reset did not clear constraints/profile_entries: %+v", afterReset)
	}
	var oldWorldUsageCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM knowledge_usage
		WHERE fact_id = $1
	`, worldFacts[0].ID).Scan(&oldWorldUsageCount); err != nil {
		t.Fatalf("count reset knowledge_usage: %v", err)
	}
	if oldWorldUsageCount != 0 {
		t.Fatalf("reset left %d knowledge_usage rows for deprecated world fact", oldWorldUsageCount)
	}

	newFact, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "New profile.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("set new profile: %v", err)
	}
	postWrite, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("get memory after new write: %v", err)
	}
	if postWrite.Version <= afterReset.Version {
		t.Fatalf("post-reset write version = %d, want > %d", postWrite.Version, afterReset.Version)
	}

	// Reconstructing at the post-reset version must return only the live profile,
	// never the deprecated pre-reset fact.
	at, err := ListActiveFactsAt(ctx, q, userID, agentID, memory.FactSubjectUser, postWrite.Version)
	if err != nil {
		t.Fatalf("ListActiveFactsAt: %v", err)
	}
	if len(at) != 1 || at[0].ID != newFact.ID {
		t.Fatalf("reconstructed facts = %+v, want only new fact %s", at, newFact.ID)
	}

	factLogs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "fact",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list fact changelog: %v", err)
	}
	var sawDeprecate bool
	for _, log := range factLogs {
		if log.Action == "delete" {
			t.Fatalf("reset fact changelog action = delete, want deprecate")
		}
		if log.Action == "deprecate" {
			sawDeprecate = true
		}
	}
	if !sawDeprecate {
		t.Fatalf("reset did not write a fact deprecate changelog entry: %+v", factLogs)
	}

	constraintLogs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "constraint",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list constraint changelog: %v", err)
	}
	if len(constraintLogs) != 2 {
		t.Fatalf("constraint changelog entries = %d, want create plus reset delete", len(constraintLogs))
	}
	if constraintLogs[0].Action != "delete" {
		t.Fatalf("reset constraint changelog action = %q, want delete", constraintLogs[0].Action)
	}
	if !constraintLogs[0].BeforeText.Valid || constraintLogs[0].BeforeText.String == "[]" {
		t.Fatalf("reset constraint changelog missing non-empty before_text: %+v", constraintLogs[0])
	}
	if !constraintLogs[0].AfterText.Valid || constraintLogs[0].AfterText.String != "[]" {
		t.Fatalf("reset constraint changelog after_text = %q, want []", constraintLogs[0].AfterText.String)
	}
}
