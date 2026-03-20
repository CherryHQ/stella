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
	ums, _ := setupUserMemoryStore(t)
	ctx := context.Background()

	content, err := ums.Get(ctx, 1, "anna")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content for new pair, got %q", content)
	}
}

func TestUserMemoryStoreSetAndGet(t *testing.T) {
	ums, _ := setupUserMemoryStore(t)
	ctx := context.Background()

	if err := ums.Set(ctx, 2, "anna", "prefers concise answers"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	content, err := ums.Get(ctx, 2, "anna")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if content != "prefers concise answers" {
		t.Errorf("content = %q, want %q", content, "prefers concise answers")
	}
}

func TestUserMemoryStoreIsolation(t *testing.T) {
	ums, _ := setupUserMemoryStore(t)
	ctx := context.Background()

	_ = ums.Set(ctx, 3, "anna", "user1 memory")
	_ = ums.Set(ctx, 4, "anna", "user2 memory")

	c1, _ := ums.Get(ctx, 3, "anna")
	c2, _ := ums.Get(ctx, 4, "anna")

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

	_ = store.CreateAgent(ctx, config.Agent{
		ID: "coder", Name: "Coder", Model: "openai/gpt-4", Workspace: "/tmp/coder", Enabled: true,
	})

	_ = ums.Set(ctx, 5, "anna", "anna memory")
	_ = ums.Set(ctx, 5, "coder", "coder memory")

	annaContent, _ := ums.Get(ctx, 5, "anna")
	coderContent, _ := ums.Get(ctx, 5, "coder")

	if annaContent != "anna memory" {
		t.Errorf("anna content = %q, want %q", annaContent, "anna memory")
	}
	if coderContent != "coder memory" {
		t.Errorf("coder content = %q, want %q", coderContent, "coder memory")
	}
}
