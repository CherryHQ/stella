package db_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
)

const testOrgID = "test-org-id"

func setupAuthStore(t *testing.T) (*appdb.AuthStore, *appdb.OIDCStore, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`INSERT OR IGNORE INTO auth_organization (id, name, source) VALUES (?, ?, ?)`, testOrgID, "Test Org", "test")
	if err != nil {
		t.Fatalf("create test org: %v", err)
	}
	return appdb.NewAuthStore(db), appdb.NewOIDCStore(db), db
}

// createUser creates an auth_user row via OIDCStore for use as FK target in tests.
func createUser(t *testing.T, oidc *appdb.OIDCStore, email string) auth.User {
	t.Helper()
	u, err := oidc.CreateUser(context.Background(), auth.User{
		ID:    uuid.NewString(),
		Email: email,
		Name:  email,
	})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", email, err)
	}
	return u
}

// seedAgent creates a test agent (needed for FK constraints on auth_user_agent).
func seedAgent(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	cs := config.NewDBStore(db)
	ctx := config.WithOrgID(context.Background(), testOrgID)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: id, Name: id, Model: "p/m", Workspace: "/tmp/" + id, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent %q: %v", id, err)
	}
}

func TestPolicyCRUD(t *testing.T) {
	t.Parallel()
	store, _, _ := setupAuthStore(t)
	ctx := context.Background()

	policy, err := store.CreatePolicy(ctx, auth.Policy{
		ID:        "system:admin-full",
		Name:      "Admin Full Access",
		Effect:    auth.EffectAllow,
		Subjects:  `{"roles":["admin"]}`,
		Actions:   `["*"]`,
		Resources: `["*"]`,
		Priority:  100,
		IsSystem:  true,
		Enabled:   true,
		OrgID:     testOrgID,
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if policy.ID != "system:admin-full" || !policy.IsSystem {
		t.Errorf("CreatePolicy = %+v", policy)
	}

	got, err := store.GetPolicy(ctx, "system:admin-full")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.Effect != auth.EffectAllow || got.Priority != 100 {
		t.Errorf("GetPolicy = %+v", got)
	}

	// Create a disabled policy.
	_, _ = store.CreatePolicy(ctx, auth.Policy{
		ID: "custom:deny", Name: "Deny", Effect: auth.EffectDeny, Enabled: false, OrgID: testOrgID,
	})

	all, _ := store.ListPolicies(ctx)
	if len(all) != 2 {
		t.Errorf("ListPolicies = %d, want 2", len(all))
	}

	enabled, _ := store.ListEnabledPolicies(ctx)
	if len(enabled) != 1 {
		t.Errorf("ListEnabledPolicies = %d, want 1", len(enabled))
	}

	// Update.
	got.Name = "Admin Full Updated"
	got.Enabled = false
	if err := store.UpdatePolicy(ctx, got); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	updated, _ := store.GetPolicy(ctx, "system:admin-full")
	if updated.Name != "Admin Full Updated" || updated.Enabled {
		t.Errorf("after update = %+v", updated)
	}

	// Delete.
	if err := store.DeletePolicy(ctx, "system:admin-full"); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	_, err = store.GetPolicy(ctx, "system:admin-full")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestUserAgentAssignment(t *testing.T) {
	t.Parallel()
	store, oidc, db := setupAuthStore(t)
	ctx := context.Background()

	user := createUser(t, oidc, "frank@example.com")
	seedAgent(t, db, "agent1")
	seedAgent(t, db, "agent2")

	// Assign.
	if err := store.AssignAgent(ctx, user.ID, "agent1"); err != nil {
		t.Fatalf("AssignAgent: %v", err)
	}
	if err := store.AssignAgent(ctx, user.ID, "agent2"); err != nil {
		t.Fatalf("AssignAgent agent2: %v", err)
	}

	// Idempotent.
	if err := store.AssignAgent(ctx, user.ID, "agent1"); err != nil {
		t.Fatalf("duplicate AssignAgent: %v", err)
	}

	agentIDs, err := store.ListUserAgentIDs(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListUserAgentIDs: %v", err)
	}
	if len(agentIDs) != 2 {
		t.Errorf("ListUserAgentIDs = %v, want 2 agents", agentIDs)
	}

	userIDs, err := store.ListAgentUserIDs(ctx, "agent1")
	if err != nil {
		t.Fatalf("ListAgentUserIDs: %v", err)
	}
	if len(userIDs) != 1 || userIDs[0] != user.ID {
		t.Errorf("ListAgentUserIDs = %v", userIDs)
	}

	// Remove.
	if err := store.RemoveAgent(ctx, user.ID, "agent1"); err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	agentIDs, _ = store.ListUserAgentIDs(ctx, user.ID)
	if len(agentIDs) != 1 || agentIDs[0] != "agent2" {
		t.Errorf("after remove = %v", agentIDs)
	}
}

// Verify that db.AuthStore satisfies auth.AuthStore interface at compile time.
var _ auth.AuthStore = (*appdb.AuthStore)(nil)

func TestUserTokenStore(t *testing.T) {
	t.Parallel()
	store, oidc, db := setupAuthStore(t)
	ctx := context.Background()

	user := createUser(t, oidc, "token-user@example.com")

	expiresAt := time.Now().Add(time.Hour).UTC()
	token, err := store.CreateUserToken(ctx, auth.UserToken{
		UserID:        user.ID,
		Name:          "sandbox",
		TokenHash:     "hash-1",
		TokenPrefix:   "stella_abcd",
		AutoGenerated: true,
		ExpiresAt:     &expiresAt,
	})
	if err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}
	if token.ID == "" || !token.AutoGenerated || token.ExpiresAt == nil {
		t.Fatalf("CreateUserToken = %+v", token)
	}

	byHash, err := store.GetUserTokenByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetUserTokenByHash: %v", err)
	}
	if byHash.ID != token.ID {
		t.Fatalf("GetUserTokenByHash ID = %s, want %s", byHash.ID, token.ID)
	}

	active, err := store.GetActiveUserTokenByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetActiveUserTokenByHash: %v", err)
	}
	if active.ID != token.ID {
		t.Fatalf("GetActiveUserTokenByHash ID = %s, want %s", active.ID, token.ID)
	}

	autoToken, err := store.GetActiveAutoUserToken(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetActiveAutoUserToken: %v", err)
	}
	if autoToken.ID != token.ID {
		t.Fatalf("GetActiveAutoUserToken ID = %s, want %s", autoToken.ID, token.ID)
	}

	rows, err := store.UpdateUserTokenLastUsed(ctx, token.ID)
	if err != nil {
		t.Fatalf("UpdateUserTokenLastUsed first: %v", err)
	}
	if rows != 1 {
		t.Fatalf("UpdateUserTokenLastUsed first rows = %d, want 1", rows)
	}
	rows, err = store.UpdateUserTokenLastUsed(ctx, token.ID)
	if err != nil {
		t.Fatalf("UpdateUserTokenLastUsed second: %v", err)
	}
	if rows != 0 {
		t.Fatalf("UpdateUserTokenLastUsed second rows = %d, want 0", rows)
	}

	rows, err = store.RevokeUserToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("RevokeUserToken: %v", err)
	}
	if rows != 1 {
		t.Fatalf("RevokeUserToken rows = %d, want 1", rows)
	}
	if _, err := store.GetActiveUserTokenByHash(ctx, "hash-1"); err == nil {
		t.Fatal("expected revoked token to be inactive")
	}

	expiredAt := time.Now().Add(-time.Hour).UTC().Format("2006-01-02 15:04:05")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO auth_user_token (user_id, name, token_hash, token_prefix, expires_at)
		VALUES (?, 'expired', 'hash-expired', 'stella_exp', ?)
	`, user.ID, expiredAt); err != nil {
		t.Fatalf("insert expired token: %v", err)
	}
	if _, err := store.GetActiveUserTokenByHash(ctx, "hash-expired"); err == nil {
		t.Fatal("expected expired token to be inactive")
	}
}

func TestRotateUserTokenGuard(t *testing.T) {
	t.Parallel()
	store, oidc, _ := setupAuthStore(t)
	ctx := context.Background()

	user := createUser(t, oidc, "rotate-token-user@example.com")

	token, err := store.CreateUserToken(ctx, auth.UserToken{
		UserID:        user.ID,
		Name:          "sandbox",
		TokenHash:     "hash-rotate",
		TokenPrefix:   "stella_rot",
		AutoGenerated: true,
	})
	if err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}
	rows, err := store.RotateUserToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("RotateUserToken first: %v", err)
	}
	if rows != 1 {
		t.Fatalf("RotateUserToken first rows = %d, want 1", rows)
	}
	rows, err = store.RotateUserToken(ctx, token.ID)
	if err != nil {
		t.Fatalf("RotateUserToken second: %v", err)
	}
	if rows != 0 {
		t.Fatalf("RotateUserToken second rows = %d, want 0", rows)
	}
}
