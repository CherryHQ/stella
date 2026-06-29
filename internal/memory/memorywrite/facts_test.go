package memorywrite

import (
	"context"
	"encoding/json"
	"testing"

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

func TestResetUserAgentMemory_KeepsVersionMonotonicAndDoesNotResurrect(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	if _, err := SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectUser, Content: "Old profile.", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("set old profile: %v", err)
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
