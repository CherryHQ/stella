package tool_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/memory/tool"
)

func setupUserMemoryTool(t *testing.T) (*tool.UserMemoryTool, *memory.UserMemoryStore, int64) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	// Create a user.
	user, err := store.UpsertUser(context.Background(), "tool-test-user", "cli", "TestUser")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	memStore := memory.NewUserMemoryStore(store)
	return tool.NewUserMemoryTool(memStore, user.ID, "anna"), memStore, user.ID
}

func TestUserMemoryToolDefinition(t *testing.T) {
	umt, _, _ := setupUserMemoryTool(t)
	def := umt.Definition()
	if def.Name != "user_memory" {
		t.Errorf("Name = %q, want %q", def.Name, "user_memory")
	}
}

func TestUserMemoryToolWrite(t *testing.T) {
	umt, memStore, userID := setupUserMemoryTool(t)
	ctx := context.Background()

	result, err := umt.Execute(ctx, map[string]any{
		"content": "## User Preferences\nPrefers concise responses\n\n## About the User\nGo developer",
	})
	if err != nil {
		t.Fatalf("Execute write: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify via store directly (tool is write-only, no read action).
	content, err := memStore.Get(ctx, userID, "anna")
	if err != nil {
		t.Fatalf("memStore.Get: %v", err)
	}
	if content != "## User Preferences\nPrefers concise responses\n\n## About the User\nGo developer" {
		t.Errorf("stored content = %q", content)
	}
}

func TestUserMemoryToolRequiresContent(t *testing.T) {
	umt, _, _ := setupUserMemoryTool(t)
	ctx := context.Background()

	_, err := umt.Execute(ctx, map[string]any{})
	if err == nil {
		t.Error("expected error for missing content")
	}
}

func TestUserMemoryToolEmptyContentErrors(t *testing.T) {
	umt, _, _ := setupUserMemoryTool(t)
	ctx := context.Background()

	_, err := umt.Execute(ctx, map[string]any{"content": ""})
	if err == nil {
		t.Error("expected error for empty content")
	}
}
