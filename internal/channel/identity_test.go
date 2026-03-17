package channel_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
)

func setupStore(t *testing.T) config.Store {
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
	return store
}

func TestResolveUserCreatesUser(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	user, err := channel.ResolveUser(ctx, store, "12345", "telegram", "Alice")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if user.ID == 0 {
		t.Error("expected non-zero user ID")
	}
	if user.ExternalID != "12345" {
		t.Errorf("ExternalID = %q, want %q", user.ExternalID, "12345")
	}
	if user.Platform != "telegram" {
		t.Errorf("Platform = %q, want %q", user.Platform, "telegram")
	}
}

func TestResolveUserIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	user1, err := channel.ResolveUser(ctx, store, "12345", "telegram", "Alice")
	if err != nil {
		t.Fatalf("first ResolveUser: %v", err)
	}
	user2, err := channel.ResolveUser(ctx, store, "12345", "telegram", "Alice")
	if err != nil {
		t.Fatalf("second ResolveUser: %v", err)
	}
	if user1.ID != user2.ID {
		t.Errorf("second call returned different ID: %d vs %d", user1.ID, user2.ID)
	}
}

func TestResolveAgentFallbackToFirstEnabled(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	user, _ := channel.ResolveUser(ctx, store, "99", "telegram", "Bob")

	chat := channel.ChatContext{Platform: "telegram", ChatID: "", IsGroup: false}
	agentID, err := channel.ResolveAgent(ctx, store, user, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	// SeedDefaults creates "anna" agent.
	if agentID != "anna" {
		t.Errorf("agentID = %q, want %q", agentID, "anna")
	}
}

func TestResolveAgentDMDefault(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// Create a second agent.
	_ = store.CreateAgent(ctx, config.Agent{
		ID:        "coder",
		Name:      "Coder",
		Model:     "openai/gpt-4",
		Workspace: "/tmp/coder",
		Enabled:   true,
	})

	user, _ := channel.ResolveUser(ctx, store, "100", "telegram", "Charlie")
	// Set default agent to coder.
	_ = store.UpdateUserDefaultAgent(ctx, user.ID, "coder")
	// Re-fetch user to get updated default.
	user, _ = channel.ResolveUser(ctx, store, "100", "telegram", "Charlie")

	chat := channel.ChatContext{Platform: "telegram", ChatID: "", IsGroup: false}
	agentID, err := channel.ResolveAgent(ctx, store, user, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "coder" {
		t.Errorf("agentID = %q, want %q", agentID, "coder")
	}
}

func TestResolveAgentGroupAssignment(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// Create a second agent.
	_ = store.CreateAgent(ctx, config.Agent{
		ID:        "writer",
		Name:      "Writer",
		Model:     "openai/gpt-4",
		Workspace: "/tmp/writer",
		Enabled:   true,
	})

	// Set group agent.
	_ = store.SetChatAgent(ctx, "telegram", "-999", "writer")

	user, _ := channel.ResolveUser(ctx, store, "200", "telegram", "Dave")
	chat := channel.ChatContext{Platform: "telegram", ChatID: "-999", IsGroup: true}
	agentID, err := channel.ResolveAgent(ctx, store, user, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "writer" {
		t.Errorf("agentID = %q, want %q", agentID, "writer")
	}
}
