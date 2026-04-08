package chatroute_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/chatroute"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
)

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
	if err := auth.SeedPolicies(context.Background(), as); err != nil {
		t.Fatalf("SeedPolicies: %v", err)
	}

	return testStores{store: store, authStore: as, db: db}
}

func TestResolveUserNoIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	resolved, err := chatroute.ResolveUser(ctx, ts.authStore, "telegram", "12345")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if resolved.User.ID != 0 {
		t.Errorf("expected User.ID=0 for unlinked user, got %d", resolved.User.ID)
	}
}

func TestResolveUserLinkedIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "alice", hash)
	_, _ = ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "telegram",
		ExternalID: "99999",
		Name:       "Alice",
	})

	resolved, err := chatroute.ResolveUser(ctx, ts.authStore, "telegram", "99999")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if resolved.User.ID != authUser.ID {
		t.Errorf("User.ID = %d, want %d", resolved.User.ID, authUser.ID)
	}
	if resolved.User.Role != auth.RoleUser {
		t.Errorf("Role = %q, want %q", resolved.User.Role, auth.RoleUser)
	}
}

func TestResolveUserDeactivated(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "inactive", hash)
	authUser.IsActive = false
	_ = ts.authStore.UpdateUser(ctx, authUser)
	_, _ = ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "qq",
		ExternalID: "111",
	})

	_, err := chatroute.ResolveUser(ctx, ts.authStore, "qq", "111")
	if err == nil {
		t.Error("expected error for deactivated user")
	}
}

func TestResolveAgentUnlinkedUserDenied(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	identity := chatroute.ResolvedIdentity{}
	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	_, err = chatroute.ResolveAgent(ctx, ts.store, ts.authStore, engine, identity, chat)
	if !errors.Is(err, chatroute.ErrAgentAccessDenied) {
		t.Fatalf("expected ErrAgentAccessDenied for unlinked user, got: %v", err)
	}
}

func TestResolveAgentFallbackToFirstEnabled(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "testuser", hash)

	identity := chatroute.ResolvedIdentity{User: authUser}
	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "anna" {
		t.Errorf("agentID = %q, want %q", agentID, "anna")
	}
}

func TestResolveAgentGroupAssignment(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID: "writer", Name: "Writer", Model: "openai/gpt-4", Workspace: "/tmp/writer", Enabled: true,
	})
	_ = ts.store.SetChatAgent(ctx, "telegram", "-999", "writer")

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "groupuser", hash)

	identity := chatroute.ResolvedIdentity{User: authUser}
	chat := chatroute.ChatContext{Platform: "telegram", ChatID: "-999", IsGroup: true}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "writer" {
		t.Errorf("agentID = %q, want %q", agentID, "writer")
	}
}

func TestTryLinkCodeSuccess(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "bob", hash)

	code := linkCodes.Generate(authUser.ID, "telegram")

	resp, ok := chatroute.TryLinkCode(ctx, ts.authStore, linkCodes, "/link "+code, "telegram", "67890", "Bob")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message")
	}
	if !strings.Contains(resp, "linked successfully") {
		t.Errorf("unexpected response: %s", resp)
	}

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

	resp, ok := chatroute.TryLinkCode(ctx, ts.authStore, linkCodes, "/link "+code, "qq", "111", "Charlie")
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

	_, ok := chatroute.TryLinkCode(ctx, ts.authStore, linkCodes, "Hello, how are you?", "telegram", "111", "Test")
	if ok {
		t.Error("expected TryLinkCode to not handle regular text")
	}
}

func TestTryLinkCodeExpiredOrInvalid(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	resp, ok := chatroute.TryLinkCode(ctx, ts.authStore, linkCodes, "/link AB12CD", "telegram", "111", "Test")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message (code format matches)")
	}
	if !strings.Contains(resp, "invalid or has expired") {
		t.Fatalf("response = %q", resp)
	}
}
