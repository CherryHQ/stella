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

	oldAfter, err := q.GetFact(ctx, oldFact.ID)
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
