package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func setupOIDCStore(t *testing.T) (*appdb.OIDCStore, context.Context) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cred_test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewOIDCStore(db), context.Background()
}

func createTestUser(t *testing.T, store *appdb.OIDCStore, ctx context.Context) auth.User {
	t.Helper()
	u, err := store.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: uuid.NewString() + "@test.example",
		Name:  "Test User",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func TestCredentialCreateAndGet(t *testing.T) {
	store, ctx := setupOIDCStore(t)
	u := createTestUser(t, store, ctx)

	cred, err := store.CreateCredential(ctx, auth.Credential{
		ID:           uuid.NewString(),
		UserID:       u.ID,
		PasswordHash: "$2a$12$fakehash",
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if cred.UserID != u.ID {
		t.Errorf("got user_id %q, want %q", cred.UserID, u.ID)
	}

	got, err := store.GetCredentialByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetCredentialByUserID: %v", err)
	}
	if got.PasswordHash != "$2a$12$fakehash" {
		t.Errorf("got hash %q, want %q", got.PasswordHash, "$2a$12$fakehash")
	}
}

func TestCredentialGetNotFound(t *testing.T) {
	store, ctx := setupOIDCStore(t)

	_, err := store.GetCredentialByUserID(ctx, "nonexistent-user")
	if !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("got error %v, want auth.ErrNotFound", err)
	}
}

func TestCredentialUpdateHash(t *testing.T) {
	store, ctx := setupOIDCStore(t)
	u := createTestUser(t, store, ctx)

	_, err := store.CreateCredential(ctx, auth.Credential{
		ID:           uuid.NewString(),
		UserID:       u.ID,
		PasswordHash: "old-hash",
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	if err := store.UpdateCredentialHash(ctx, u.ID, "new-hash"); err != nil {
		t.Fatalf("UpdateCredentialHash: %v", err)
	}

	got, err := store.GetCredentialByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetCredentialByUserID: %v", err)
	}
	if got.PasswordHash != "new-hash" {
		t.Errorf("got hash %q, want %q", got.PasswordHash, "new-hash")
	}
}

func TestCredentialDelete(t *testing.T) {
	store, ctx := setupOIDCStore(t)
	u := createTestUser(t, store, ctx)

	_, err := store.CreateCredential(ctx, auth.Credential{
		ID:           uuid.NewString(),
		UserID:       u.ID,
		PasswordHash: "some-hash",
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	if err := store.DeleteCredential(ctx, u.ID); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}

	_, err = store.GetCredentialByUserID(ctx, u.ID)
	if !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("after delete got error %v, want auth.ErrNotFound", err)
	}
}

func TestBackfillCredentials(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backfill_cred.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()

	// Insert a legacy user with a password hash.
	userID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO auth_users (id, username, password_hash) VALUES (?, ?, ?)`,
		userID, "legacyuser@example.com", "$2a$12$legacyhash"); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}

	// Run OIDC backfill first (creates auth_user row).
	if _, err := appdb.BackfillOIDCTables(ctx, db); err != nil {
		t.Fatalf("BackfillOIDCTables: %v", err)
	}

	// Now backfill credentials.
	n, err := appdb.BackfillCredentials(ctx, db)
	if err != nil {
		t.Fatalf("BackfillCredentials: %v", err)
	}
	if n != 1 {
		t.Errorf("BackfillCredentials returned %d, want 1", n)
	}

	// Verify credential was created.
	store := appdb.NewOIDCStore(db)
	cred, err := store.GetCredentialByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("GetCredentialByUserID after backfill: %v", err)
	}
	if cred.PasswordHash != "$2a$12$legacyhash" {
		t.Errorf("got hash %q, want %q", cred.PasswordHash, "$2a$12$legacyhash")
	}

	// Idempotency: running again should backfill 0.
	n2, err := appdb.BackfillCredentials(ctx, db)
	if err != nil {
		t.Fatalf("BackfillCredentials (2nd run): %v", err)
	}
	if n2 != 0 {
		t.Errorf("2nd BackfillCredentials returned %d, want 0", n2)
	}
}
