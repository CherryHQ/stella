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

func setupUserMemoryTool(t *testing.T) *tool.UserMemoryTool {
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
	return tool.NewUserMemoryTool(memStore, user.ID, "anna")
}

func TestUserMemoryToolDefinition(t *testing.T) {
	umt := setupUserMemoryTool(t)
	def := umt.Definition()
	if def.Name != "user_memory" {
		t.Errorf("Name = %q, want %q", def.Name, "user_memory")
	}
}

func TestUserMemoryToolReadEmpty(t *testing.T) {
	umt := setupUserMemoryTool(t)
	ctx := context.Background()

	result, err := umt.Execute(ctx, map[string]any{"action": "read"})
	if err != nil {
		t.Fatalf("Execute read: %v", err)
	}
	if result != "No user memory stored yet." {
		t.Errorf("result = %q, want empty message", result)
	}
}

func TestUserMemoryToolWriteAndRead(t *testing.T) {
	umt := setupUserMemoryTool(t)
	ctx := context.Background()

	// Write.
	result, err := umt.Execute(ctx, map[string]any{
		"action":  "write",
		"content": "likes Go and Rust",
	})
	if err != nil {
		t.Fatalf("Execute write: %v", err)
	}
	if result != "User memory updated successfully." {
		t.Errorf("write result = %q", result)
	}

	// Read back.
	result, err = umt.Execute(ctx, map[string]any{"action": "read"})
	if err != nil {
		t.Fatalf("Execute read: %v", err)
	}
	if result != "likes Go and Rust" {
		t.Errorf("read result = %q, want %q", result, "likes Go and Rust")
	}
}

func TestUserMemoryToolWriteRequiresContent(t *testing.T) {
	umt := setupUserMemoryTool(t)
	ctx := context.Background()

	_, err := umt.Execute(ctx, map[string]any{"action": "write"})
	if err == nil {
		t.Error("expected error for write without content")
	}
}

func TestUserMemoryToolInvalidAction(t *testing.T) {
	umt := setupUserMemoryTool(t)
	ctx := context.Background()

	_, err := umt.Execute(ctx, map[string]any{"action": "delete"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}
