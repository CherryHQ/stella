package memorywrite

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// setupTestDB creates a test database and returns the db, queries, userID, agentID, and cleanup.
func setupTestDB(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, string, string, func()) {
	t.Helper()
	db := dbtest.New(t)

	q := sqlc.New(db)
	ctx := context.Background()

	// Create test user
	oidcStore := appdb.NewOIDCStore(db)
	u, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "testuser@test.local",
		Name:  "testuser",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Create test agent (required for FK constraint)
	agentID := "test-agent-1"
	store := cfgstore.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{
		ID:      agentID,
		Name:    "Test Agent",
		Model:   "anthropic/claude",
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	cleanup := func() {}

	return db, q, u.ID, agentID, cleanup
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
