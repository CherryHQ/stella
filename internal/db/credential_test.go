package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

func setupOIDCStore(t *testing.T) (*OIDCStore, context.Context) {
	t.Helper()
	db := newTestDB(t)
	return NewOIDCStore(db), context.Background()
}

func createTestUser(t *testing.T, store *OIDCStore, ctx context.Context) auth.User {
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

	_, err := store.GetCredentialByUserID(ctx, uuid.NewString())
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
