package memorywrite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
)

// setupTestDB creates a test database and returns the db, queries, userID, agentID, and cleanup.
func setupTestDB(t *testing.T) (*sql.DB, *sqlc.Queries, string, string, func()) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}

	q := sqlc.New(db)
	ctx := context.Background()

	// Create test user
	authStore := appdb.NewAuthStore(db)
	u, err := authStore.CreateUser(ctx, "testuser", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Create test agent (required for FK constraint)
	agentID := "test-agent-1"
	store := config.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{
		ID:      agentID,
		Name:    "Test Agent",
		Model:   "anthropic/claude",
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	cleanup := func() {
		_ = db.Close()
	}

	return db, q, u.ID, agentID, cleanup
}

func TestSetProfile_create(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceAgent)

	content := "Test profile content"
	err := SetProfile(ctx, db, q, userID, agentID, content)
	if err != nil {
		t.Fatalf("SetProfile: %v", err)
	}

	// Verify the profile was created
	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		t.Fatalf("GetUserAgentMemory: %v", err)
	}
	if row.Content != content {
		t.Errorf("Content = %q, want %q", row.Content, content)
	}
	if row.Version != 1 {
		t.Errorf("Version = %d, want 1", row.Version)
	}
}

func TestSetProfile_update(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceAgent)

	// First create
	content1 := "Initial content"
	err := SetProfile(ctx, db, q, userID, agentID, content1)
	if err != nil {
		t.Fatalf("SetProfile (create): %v", err)
	}

	// Then update
	content2 := "Updated content"
	err = SetProfile(ctx, db, q, userID, agentID, content2)
	if err != nil {
		t.Fatalf("SetProfile (update): %v", err)
	}

	// Verify the update
	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		t.Fatalf("GetUserAgentMemory: %v", err)
	}
	if row.Content != content2 {
		t.Errorf("Content = %q, want %q", row.Content, content2)
	}
	if row.Version != 2 {
		t.Errorf("Version = %d, want 2", row.Version)
	}
}

func TestSetAgentSoul(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	soul := "Agent soul content"
	err := SetAgentSoul(ctx, db, q, userID, agentID, soul)
	if err != nil {
		t.Fatalf("SetAgentSoul: %v", err)
	}

	// Verify
	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		t.Fatalf("GetUserAgentMemory: %v", err)
	}
	if row.Soul != soul {
		t.Errorf("Soul = %q, want %q", row.Soul, soul)
	}
}

func TestDeleteProfile(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceSystem)

	// First create
	content := "Profile to delete"
	err := SetProfile(ctx, db, q, userID, agentID, content)
	if err != nil {
		t.Fatalf("SetProfile: %v", err)
	}

	// Then delete
	err = DeleteProfile(ctx, db, q, userID, agentID)
	if err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}

	// Verify deleted
	_, err = q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err == nil {
		t.Error("expected record to be deleted, but it still exists")
	}
}

func TestDeleteProfile_noExisting(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceSystem)

	// Delete without creating first should not error
	err := DeleteProfile(ctx, db, q, userID, agentID)
	if err != nil {
		t.Fatalf("DeleteProfile (no existing): %v", err)
	}
}

func TestGetConstraints_empty(t *testing.T) {
	_, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	constraints, err := GetConstraints(ctx, q, userID, agentID)
	if err != nil {
		t.Fatalf("GetConstraints: %v", err)
	}
	if len(constraints) != 0 {
		t.Errorf("expected empty constraints, got %d", len(constraints))
	}
}

func TestAddConstraint(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceAgent)

	// Add first constraint
	constraints, err := AddConstraint(ctx, db, q, userID, agentID, "Constraint 1")
	if err != nil {
		t.Fatalf("AddConstraint: %v", err)
	}
	if len(constraints) != 1 {
		t.Errorf("expected 1 constraint, got %d", len(constraints))
	}
	if constraints[0].Text != "Constraint 1" {
		t.Errorf("Text = %q, want %q", constraints[0].Text, "Constraint 1")
	}

	// Add second constraint
	constraints, err = AddConstraint(ctx, db, q, userID, agentID, "Constraint 2")
	if err != nil {
		t.Fatalf("AddConstraint (second): %v", err)
	}
	if len(constraints) != 2 {
		t.Errorf("expected 2 constraints, got %d", len(constraints))
	}

	// Verify via GetConstraints
	stored, err := GetConstraints(ctx, q, userID, agentID)
	if err != nil {
		t.Fatalf("GetConstraints: %v", err)
	}
	if len(stored) != 2 {
		t.Errorf("stored constraints = %d, want 2", len(stored))
	}
}

func TestRemoveConstraint(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceAgent)

	// Add constraints
	_, err := AddConstraint(ctx, db, q, userID, agentID, "Constraint 1")
	if err != nil {
		t.Fatalf("AddConstraint: %v", err)
	}
	constraints, err := AddConstraint(ctx, db, q, userID, agentID, "Constraint 2")
	if err != nil {
		t.Fatalf("AddConstraint (second): %v", err)
	}

	idToRemove := constraints[0].ID

	// Remove first constraint
	remaining, err := RemoveConstraint(ctx, db, q, userID, agentID, idToRemove)
	if err != nil {
		t.Fatalf("RemoveConstraint: %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("expected 1 remaining constraint, got %d", len(remaining))
	}
	if remaining[0].Text != "Constraint 2" {
		t.Errorf("remaining text = %q, want %q", remaining[0].Text, "Constraint 2")
	}
}

func TestParseConstraintsJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int // number of constraints
		wantErr  bool
	}{
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
		{
			name:     "empty array",
			input:    "[]",
			expected: 0,
		},
		{
			name:     "null",
			input:    "null",
			expected: 0,
		},
		{
			name:     "valid single constraint",
			input:    `[{"id":"c1","text":"test","created_at":"2024-01-01T00:00:00Z"}]`,
			expected: 1,
		},
		{
			name:     "valid multiple constraints",
			input:    `[{"id":"c1","text":"test1","created_at":"2024-01-01T00:00:00Z"},{"id":"c2","text":"test2","created_at":"2024-01-02T00:00:00Z"}]`,
			expected: 2,
		},
		{
			name:    "invalid JSON",
			input:   "{invalid",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			constraints, err := ParseConstraintsJSON(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(constraints) != tc.expected {
				t.Errorf("got %d constraints, want %d", len(constraints), tc.expected)
			}
		})
	}
}

func TestChangelogCreatedForProfile(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceAgent)

	// Create profile
	err := SetProfile(ctx, db, q, userID, agentID, "Profile content")
	if err != nil {
		t.Fatalf("SetProfile: %v", err)
	}

	// Check changelog
	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "profile",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMemoryChangelog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 changelog entry, got %d", len(logs))
	}
	if logs[0].Scope != "profile" {
		t.Errorf("Scope = %q, want profile", logs[0].Scope)
	}
	if logs[0].Action != "create" {
		t.Errorf("Action = %q, want create", logs[0].Action)
	}
	if logs[0].Source != string(memory.SourceAgent) {
		t.Errorf("Source = %q, want %q", logs[0].Source, memory.SourceAgent)
	}
}

func TestChangelogCreatedForConstraint(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := memory.WithChangeSource(context.Background(), memory.SourceReflect)

	// Add constraint
	_, err := AddConstraint(ctx, db, q, userID, agentID, "Test constraint")
	if err != nil {
		t.Fatalf("AddConstraint: %v", err)
	}

	// Check changelog for constraint scope
	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   "constraint",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMemoryChangelog: %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("expected 1 constraint changelog entry, got %d", len(logs))
	}
	if logs[0].Action != "create" {
		t.Errorf("Action = %q, want create", logs[0].Action)
	}
}
