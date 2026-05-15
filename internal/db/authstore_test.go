package db_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func setupAuthStore(t *testing.T) (*appdb.AuthStore, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewAuthStore(db), db
}

// seedAgent creates a test agent (needed for FK constraints on auth_user_agents).
func seedAgent(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	cs := config.NewDBStore(db)
	if err := cs.CreateAgent(context.Background(), config.Agent{
		ID: id, Name: id, Model: "p/m", Workspace: "/tmp/" + id, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent %q: %v", id, err)
	}
}

func TestUserCRUD(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	// Create.
	user, err := store.CreateUser(ctx, "alice", "hash123")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Username != "alice" || !user.IsActive {
		t.Errorf("CreateUser = %+v", user)
	}
	if user.ID == 0 {
		t.Error("expected non-zero ID")
	}

	// Get by ID.
	got, err := store.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("GetUser username = %q", got.Username)
	}

	// Get by username.
	got, err = store.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("GetUserByUsername ID = %d, want %d", got.ID, user.ID)
	}

	// List.
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("ListUsers = %d users, want 1", len(users))
	}

	// Count.
	count, err := store.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 1 {
		t.Errorf("CountUsers = %d, want 1", count)
	}

	// Update.
	got.Username = "alice2"
	got.IsActive = false
	got.PasswordHash = "newhash"
	if err := store.UpdateUser(ctx, got); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	updated, _ := store.GetUser(ctx, user.ID)
	if updated.Username != "alice2" || updated.IsActive || updated.PasswordHash != "newhash" {
		t.Errorf("after update = %+v", updated)
	}

	// Delete.
	if err := store.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	_, err = store.GetUser(ctx, user.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestUserDuplicateUsername(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	_, err := store.CreateUser(ctx, "bob", "hash")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = store.CreateUser(ctx, "bob", "hash2")
	if err == nil {
		t.Error("expected error on duplicate username")
	}
}

func TestUserRole(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "carol", "hash")

	// Default role is "user".
	if user.Role != auth.RoleUser {
		t.Errorf("default role = %q, want %q", user.Role, auth.RoleUser)
	}

	// Promote to admin.
	if err := store.UpdateUserRole(ctx, user.ID, auth.RoleAdmin); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	got, _ := store.GetUser(ctx, user.ID)
	if got.Role != auth.RoleAdmin {
		t.Errorf("after promote role = %q, want %q", got.Role, auth.RoleAdmin)
	}

	// Demote back to user.
	if err := store.UpdateUserRole(ctx, user.ID, auth.RoleUser); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	got, _ = store.GetUser(ctx, user.ID)
	if got.Role != auth.RoleUser {
		t.Errorf("after demote role = %q, want %q", got.Role, auth.RoleUser)
	}
}

func TestIdentityCRUD(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "dave", "hash")

	identity, err := store.CreateIdentity(ctx, auth.Identity{
		UserID: user.ID, Platform: "telegram", ExternalID: "tg-123", Name: "Dave TG",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if identity.ID == "" || identity.Platform != "telegram" {
		t.Errorf("CreateIdentity = %+v", identity)
	}

	got, err := store.GetIdentity(ctx, identity.ID)
	if err != nil {
		t.Fatalf("GetIdentity: %v", err)
	}
	if got.ExternalID != "tg-123" {
		t.Errorf("GetIdentity external_id = %q", got.ExternalID)
	}

	got, err = store.GetIdentityByPlatform(ctx, "telegram", "tg-123")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform: %v", err)
	}
	if got.UserID != user.ID {
		t.Errorf("GetIdentityByPlatform user_id = %d", got.UserID)
	}

	identities, err := store.ListIdentitiesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListIdentitiesByUser: %v", err)
	}
	if len(identities) != 1 {
		t.Errorf("ListIdentitiesByUser = %d", len(identities))
	}

	if err := store.DeleteIdentity(ctx, identity.ID); err != nil {
		t.Fatalf("DeleteIdentity: %v", err)
	}
	_, err = store.GetIdentity(ctx, identity.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestIdentityUniqueConstraint(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "eve", "hash")
	_, _ = store.CreateIdentity(ctx, auth.Identity{
		UserID: user.ID, Platform: "telegram", ExternalID: "tg-456",
	})

	_, err := store.CreateIdentity(ctx, auth.Identity{
		UserID: user.ID, Platform: "telegram", ExternalID: "tg-456",
	})
	if err == nil {
		t.Error("expected error on duplicate platform+external_id")
	}
}

func TestUpdateIdentityExternalID(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "frank", "hash")
	identity, err := store.CreateIdentity(ctx, auth.Identity{
		UserID: user.ID, Platform: "feishu", ExternalID: "ou_legacy",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	if err := store.UpdateIdentityExternalID(ctx, identity.ID, "on_stable"); err != nil {
		t.Fatalf("UpdateIdentityExternalID: %v", err)
	}

	got, err := store.GetIdentityByPlatform(ctx, "feishu", "on_stable")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform: %v", err)
	}
	if got.ID != identity.ID {
		t.Fatalf("updated identity id = %s, want %s", got.ID, identity.ID)
	}
}

func TestPolicyCRUD(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
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
		ID: "custom:deny", Name: "Deny", Effect: auth.EffectDeny, Enabled: false,
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
	store, db := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "frank", "hash")
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

func TestSessionCRUD(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "grace", "hash")

	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	sess, err := store.CreateSession(ctx, auth.Session{
		ID: "sess-abc", UserID: user.ID, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID != "sess-abc" || sess.UserID != user.ID {
		t.Errorf("CreateSession = %+v", sess)
	}

	got, err := store.GetSession(ctx, "sess-abc")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.UserID != user.ID {
		t.Errorf("GetSession user_id = %d", got.UserID)
	}

	// Update expiry.
	newExpiry := time.Now().UTC().Add(14 * 24 * time.Hour)
	if err := store.UpdateSessionExpiry(ctx, "sess-abc", newExpiry); err != nil {
		t.Fatalf("UpdateSessionExpiry: %v", err)
	}

	// Delete.
	if err := store.DeleteSession(ctx, "sess-abc"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	_, err = store.GetSession(ctx, "sess-abc")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "hank", "hash")

	// Create an expired session.
	past := time.Now().UTC().Add(-1 * time.Hour)
	_, _ = store.CreateSession(ctx, auth.Session{
		ID: "expired", UserID: user.ID, ExpiresAt: past,
	})
	// Create a valid session.
	future := time.Now().UTC().Add(1 * time.Hour)
	_, _ = store.CreateSession(ctx, auth.Session{
		ID: "valid", UserID: user.ID, ExpiresAt: future,
	})

	if err := store.DeleteExpiredSessions(ctx); err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}

	_, err := store.GetSession(ctx, "expired")
	if err == nil {
		t.Error("expired session should be deleted")
	}

	_, err = store.GetSession(ctx, "valid")
	if err != nil {
		t.Error("valid session should still exist")
	}
}

func TestDeleteUserSessions(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "iris", "hash")
	future := time.Now().UTC().Add(1 * time.Hour)
	_, _ = store.CreateSession(ctx, auth.Session{
		ID: "s1", UserID: user.ID, ExpiresAt: future,
	})
	_, _ = store.CreateSession(ctx, auth.Session{
		ID: "s2", UserID: user.ID, ExpiresAt: future,
	})

	if err := store.DeleteUserSessions(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUserSessions: %v", err)
	}

	_, err := store.GetSession(ctx, "s1")
	if err == nil {
		t.Error("s1 should be deleted")
	}
	_, err = store.GetSession(ctx, "s2")
	if err == nil {
		t.Error("s2 should be deleted")
	}
}

func TestCascadeDeleteUser(t *testing.T) {
	t.Parallel()
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, "jack", "hash")
	_, _ = store.CreateIdentity(ctx, auth.Identity{
		UserID: user.ID, Platform: "qq", ExternalID: "qq-1",
	})
	future := time.Now().UTC().Add(1 * time.Hour)
	_, _ = store.CreateSession(ctx, auth.Session{
		ID: "csess", UserID: user.ID, ExpiresAt: future,
	})

	// Delete user should cascade.
	if err := store.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	identities, _ := store.ListIdentitiesByUser(ctx, user.ID)
	if len(identities) != 0 {
		t.Error("identities should be cascade deleted")
	}

	_, err := store.GetSession(ctx, "csess")
	if err == nil {
		t.Error("session should be cascade deleted")
	}
}

// Verify that db.AuthStore satisfies auth.AuthStore interface at compile time.
var _ auth.AuthStore = (*appdb.AuthStore)(nil)

func TestUserTokenStore(t *testing.T) {
	t.Parallel()
	store, db := setupAuthStore(t)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, "token-user", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
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
		INSERT INTO auth_user_tokens (user_id, name, token_hash, token_prefix, expires_at)
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
	store, _ := setupAuthStore(t)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, "rotate-token-user", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
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
