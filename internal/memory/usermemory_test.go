package memory_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
)

func setupUserMemoryStore(t *testing.T) (*memory.UserMemoryStore, config.Store) {
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
	return memory.NewUserMemoryStore(store), store
}

func TestUserMemoryStoreEmptyOnNew(t *testing.T) {
	ums, store := setupUserMemoryStore(t)
	ctx := context.Background()

	// Create a user so we have a valid user ID.
	user, err := store.UpsertUser(ctx, "ext1", "telegram", "Alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	content, err := ums.Get(ctx, user.ID, "anna")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content for new pair, got %q", content)
	}
}

func TestUserMemoryStoreSetAndGet(t *testing.T) {
	ums, store := setupUserMemoryStore(t)
	ctx := context.Background()

	user, _ := store.UpsertUser(ctx, "ext2", "telegram", "Bob")

	if err := ums.Set(ctx, user.ID, "anna", "prefers concise answers"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	content, err := ums.Get(ctx, user.ID, "anna")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if content != "prefers concise answers" {
		t.Errorf("content = %q, want %q", content, "prefers concise answers")
	}
}

func TestUserMemoryStoreIsolation(t *testing.T) {
	ums, store := setupUserMemoryStore(t)
	ctx := context.Background()

	user1, _ := store.UpsertUser(ctx, "ext3", "telegram", "Charlie")
	user2, _ := store.UpsertUser(ctx, "ext4", "telegram", "Dave")

	// Set different memory for each user.
	_ = ums.Set(ctx, user1.ID, "anna", "user1 memory")
	_ = ums.Set(ctx, user2.ID, "anna", "user2 memory")

	c1, _ := ums.Get(ctx, user1.ID, "anna")
	c2, _ := ums.Get(ctx, user2.ID, "anna")

	if c1 != "user1 memory" {
		t.Errorf("user1 content = %q, want %q", c1, "user1 memory")
	}
	if c2 != "user2 memory" {
		t.Errorf("user2 content = %q, want %q", c2, "user2 memory")
	}
}

func TestUserMemoryStoreAgentIsolation(t *testing.T) {
	ums, store := setupUserMemoryStore(t)
	ctx := context.Background()

	// Create a second agent.
	_ = store.CreateAgent(ctx, config.Agent{
		ID: "coder", Name: "Coder",
		Model: "openai/gpt-4", Workspace: "/tmp/coder", Enabled: true,
	})

	user, _ := store.UpsertUser(ctx, "ext5", "telegram", "Eve")

	_ = ums.Set(ctx, user.ID, "anna", "anna memory")
	_ = ums.Set(ctx, user.ID, "coder", "coder memory")

	annaContent, _ := ums.Get(ctx, user.ID, "anna")
	coderContent, _ := ums.Get(ctx, user.ID, "coder")

	if annaContent != "anna memory" {
		t.Errorf("anna content = %q, want %q", annaContent, "anna memory")
	}
	if coderContent != "coder memory" {
		t.Errorf("coder content = %q, want %q", coderContent, "coder memory")
	}
}
