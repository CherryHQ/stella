package channel_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/auth"
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

type testStores struct {
	store     config.Store
	authStore auth.AuthStore
	db        *sql.DB
}

func setupStores(t *testing.T) testStores {
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

	as := appdb.NewAuthStore(db)
	if err := auth.SeedRolesAndPolicies(context.Background(), as); err != nil {
		t.Fatalf("SeedRolesAndPolicies: %v", err)
	}

	return testStores{store: store, authStore: as, db: db}
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

// --- Auth-aware identity resolution tests ---

func TestResolveUserWithAuthNoAutoMigrate(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	// Unlinked user should resolve with settings_users but no auth user.
	resolved, err := channel.ResolveUserWithAuth(ctx, ts.store, ts.authStore, "12345", "telegram", "Alice")
	if err != nil {
		t.Fatalf("ResolveUserWithAuth: %v", err)
	}
	if resolved.AuthUserID != 0 {
		t.Errorf("expected AuthUserID=0 for unlinked user, got %d", resolved.AuthUserID)
	}
	if resolved.ExternalID != "12345" {
		t.Errorf("ExternalID = %q, want %q", resolved.ExternalID, "12345")
	}

	// Verify no auth_identity was created.
	_, err = ts.authStore.GetIdentityByPlatform(ctx, "telegram", "12345")
	if err == nil {
		t.Error("expected no identity for unlinked user, but one was found")
	}
}

func TestResolveUserWithAuthLinkedIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	// Pre-create auth user and identity.
	hash, _ := auth.HashPassword("testpass1")
	authUser, err := ts.authStore.CreateUser(ctx, "alice", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = ts.authStore.AssignRole(ctx, authUser.ID, auth.RoleAdmin)
	_ = ts.authStore.AssignRole(ctx, authUser.ID, auth.RoleUser)
	_, err = ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "telegram",
		ExternalID: "99999",
		Name:       "Alice",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	// Resolve should find the linked identity.
	resolved, err := channel.ResolveUserWithAuth(ctx, ts.store, ts.authStore, "99999", "telegram", "Alice")
	if err != nil {
		t.Fatalf("ResolveUserWithAuth: %v", err)
	}
	if resolved.AuthUserID != authUser.ID {
		t.Errorf("AuthUserID = %d, want %d", resolved.AuthUserID, authUser.ID)
	}
	if len(resolved.Roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(resolved.Roles))
	}
}

func TestResolveUserWithAuthIdempotent(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	// First call auto-migrates.
	r1, err := channel.ResolveUserWithAuth(ctx, ts.store, ts.authStore, "555", "qq", "Bob")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call should find the existing identity.
	r2, err := channel.ResolveUserWithAuth(ctx, ts.store, ts.authStore, "555", "qq", "Bob")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if r1.AuthUserID != r2.AuthUserID {
		t.Errorf("AuthUserID changed: %d vs %d", r1.AuthUserID, r2.AuthUserID)
	}
}

func TestTryLinkCodeSuccess(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	// Create an auth user.
	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "bob", hash)

	// Generate a code.
	code := linkCodes.Generate(authUser.ID, "telegram")

	// Try to link using /link command.
	resp, ok := channel.TryLinkCode(ctx, ts.authStore, linkCodes, "/link "+code, "telegram", "67890", "Bob")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message")
	}
	if !strings.Contains(resp, "linked successfully") {
		t.Errorf("unexpected response: %s", resp)
	}

	// Verify identity was created.
	identity, err := ts.authStore.GetIdentityByPlatform(ctx, "telegram", "67890")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform: %v", err)
	}
	if identity.UserID != authUser.ID {
		t.Errorf("identity.UserID = %d, want %d", identity.UserID, authUser.ID)
	}
}

func TestTryLinkCodeWrongPlatform(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "charlie", hash)

	code := linkCodes.Generate(authUser.ID, "telegram")

	// Try with wrong platform using /link command.
	resp, ok := channel.TryLinkCode(ctx, ts.authStore, linkCodes, "/link "+code, "qq", "111", "Charlie")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message")
	}
	if resp == "" {
		t.Error("expected non-empty error response")
	}
}

func TestTryLinkCodeNotACode(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	// Regular text should not be handled.
	_, ok := channel.TryLinkCode(ctx, ts.authStore, linkCodes, "Hello, how are you?", "telegram", "111", "Test")
	if ok {
		t.Error("expected TryLinkCode to not handle regular text")
	}
}

func TestTryLinkCodeExpiredOrInvalid(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	// Try with a valid-looking but non-existent code via /link command.
	resp, ok := channel.TryLinkCode(ctx, ts.authStore, linkCodes, "/link AB12CD", "telegram", "111", "Test")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message (code format matches)")
	}
	if resp == "" {
		t.Error("expected error response for invalid code")
	}
}
