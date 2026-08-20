package memorywrite_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func openTestDB(t *testing.T) (*pgxpool.Pool, *sqlc.Queries) {
	t.Helper()
	db := dbtest.New(t)
	return db, sqlc.New(db)
}

// seedUserAgent inserts a test user (uuid id) and agent (text slug id) and
// returns the user id so callers thread the same id through every write.
func seedUserAgent(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	userID := uuid.NewString()
	if _, err := db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ($1, 'u1@test.local')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO agent (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled) VALUES ('a1', 'agent1', '', '', '', '', '/tmp', 'user', $1, true)`, userID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return userID
}

func TestAddProfileEntry(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	userID := seedUserAgent(t, db)

	ctx = memory.WithChangeSource(ctx, memory.SourceAgent)
	entries, err := memorywrite.AddProfileEntry(ctx, db, q, userID, "a1", "likes coffee")
	if err != nil {
		t.Fatalf("add entry: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Text != "likes coffee" {
		t.Fatalf("text = %q, want %q", entries[0].Text, "likes coffee")
	}
	if entries[0].Source != "auto" {
		t.Fatalf("source = %q, want %q", entries[0].Source, "auto")
	}
	if entries[0].CreatedAt == "" {
		t.Fatal("created_at should be set")
	}
}

func TestAddMultipleProfileEntries(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	userID := seedUserAgent(t, db)

	ctx = memory.WithChangeSource(ctx, memory.SourceAgent)
	if _, err := memorywrite.AddProfileEntry(ctx, db, q, userID, "a1", "fact 1"); err != nil {
		t.Fatalf("add first entry: %v", err)
	}
	entries, err := memorywrite.AddProfileEntry(ctx, db, q, userID, "a1", "fact 2")
	if err != nil {
		t.Fatalf("add second entry: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
}

func TestGetProfileEntriesEmpty(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	userID := seedUserAgent(t, db)

	entries, err := memorywrite.GetProfileEntries(ctx, q, userID, "a1")
	if err != nil {
		t.Fatalf("get entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries, want 0", len(entries))
	}
}

func TestSetProfileDoesNotEraseEntries(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	userID := seedUserAgent(t, db)

	ctx = memory.WithChangeSource(ctx, memory.SourceAgent)
	if _, err := memorywrite.AddProfileEntry(ctx, db, q, userID, "a1", "auto fact"); err != nil {
		t.Fatalf("add entry: %v", err)
	}

	ctx = memory.WithChangeSource(ctx, memory.SourceUser)
	if _, err := memorywrite.SetSingletonFact(ctx, db, q, memory.FactWrite{
		UserID:  userID,
		AgentID: "a1",
		Subject: memory.FactSubjectUser,
		Content: "manual profile text",
		Source:  memory.SourceManual,
	}); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	entries, err := memorywrite.GetProfileEntries(ctx, q, userID, "a1")
	if err != nil {
		t.Fatalf("get entries after PATCH: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries after PATCH, want 1 — PATCH erased auto entries", len(entries))
	}
	if entries[0].Text != "auto fact" {
		t.Fatalf("entry text = %q, want %q", entries[0].Text, "auto fact")
	}
}

func TestProfileEntryVersionBumps(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	userID := seedUserAgent(t, db)

	ctx = memory.WithChangeSource(ctx, memory.SourceAgent)
	if _, err := memorywrite.AddProfileEntry(ctx, db, q, userID, "a1", "fact 1"); err != nil {
		t.Fatalf("add first entry: %v", err)
	}
	row1, _ := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: "a1"})

	if _, err := memorywrite.AddProfileEntry(ctx, db, q, userID, "a1", "fact 2"); err != nil {
		t.Fatalf("add second entry: %v", err)
	}
	row2, _ := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: "a1"})

	if row2.Version <= row1.Version {
		t.Fatalf("version should increment: %d <= %d", row2.Version, row1.Version)
	}
}
