package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func setupOIDCIssuerStore(t *testing.T) (*appdb.OIDCStore, context.Context) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "issuer_test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewOIDCStore(db), context.Background()
}

func createIssuerTestUser(t *testing.T, store *appdb.OIDCStore, ctx context.Context) auth.User {
	t.Helper()
	u, err := store.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: uuid.NewString() + "@issuer.example",
		Name:  "Issuer Test User",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func TestOIDCCodeCreateAndConsume(t *testing.T) {
	store, ctx := setupOIDCIssuerStore(t)
	u := createIssuerTestUser(t, store, ctx)

	code, err := store.CreateOIDCCode(ctx, auth.OIDCCode{
		ID:            uuid.NewString(),
		CodeHash:      "hash1",
		UserID:        u.ID,
		OrgID:         "org1",
		ClientID:      "client1",
		RedirectURI:   "http://localhost/callback",
		Scopes:        []string{"openid", "email"},
		Nonce:         "nonce1",
		PKCEChallenge: "challenge1",
		PKCEMethod:    "S256",
		ExpiresAt:     time.Now().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateOIDCCode: %v", err)
	}
	if code.CodeHash != "hash1" {
		t.Errorf("code_hash %q, want hash1", code.CodeHash)
	}

	// Consume succeeds.
	consumed, err := store.ConsumeOIDCCode(ctx, "hash1")
	if err != nil {
		t.Fatalf("ConsumeOIDCCode: %v", err)
	}
	if consumed.ConsumedAt == nil {
		t.Error("consumed_at should be set")
	}
	if len(consumed.Scopes) != 2 {
		t.Errorf("scopes len %d, want 2", len(consumed.Scopes))
	}

	// Second consume returns ErrAlreadyConsumed.
	_, err = store.ConsumeOIDCCode(ctx, "hash1")
	if !errors.Is(err, auth.ErrAlreadyConsumed) {
		t.Errorf("second consume: got %v, want ErrAlreadyConsumed", err)
	}
}

func TestOIDCCodeNotFound(t *testing.T) {
	store, ctx := setupOIDCIssuerStore(t)
	_, err := store.ConsumeOIDCCode(ctx, "no-such-hash")
	if !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("not found: got %v, want ErrNotFound", err)
	}
}

func TestOIDCCodeExpired(t *testing.T) {
	store, ctx := setupOIDCIssuerStore(t)
	u := createIssuerTestUser(t, store, ctx)

	_, err := store.CreateOIDCCode(ctx, auth.OIDCCode{
		ID:        uuid.NewString(),
		CodeHash:  "expiredhash",
		UserID:    u.ID,
		ClientID:  "client",
		ExpiresAt: time.Now().Add(-time.Minute), // already expired
	})
	if err != nil {
		t.Fatalf("CreateOIDCCode: %v", err)
	}

	_, err = store.ConsumeOIDCCode(ctx, "expiredhash")
	if !errors.Is(err, auth.ErrExpired) {
		t.Errorf("expired: got %v, want ErrExpired", err)
	}
}

func TestOIDCAccessTokenCreateAndGet(t *testing.T) {
	store, ctx := setupOIDCIssuerStore(t)
	u := createIssuerTestUser(t, store, ctx)

	tok, err := store.CreateOIDCAccessToken(ctx, auth.OIDCAccessToken{
		ID:        uuid.NewString(),
		TokenHash: "tokenhash1",
		UserID:    u.ID,
		OrgID:     "org1",
		ClientID:  "client1",
		Scopes:    []string{"openid", "email", "profile"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateOIDCAccessToken: %v", err)
	}
	if tok.TokenHash != "tokenhash1" {
		t.Errorf("token_hash %q, want tokenhash1", tok.TokenHash)
	}

	got, err := store.GetOIDCAccessTokenByHash(ctx, "tokenhash1")
	if err != nil {
		t.Fatalf("GetOIDCAccessTokenByHash: %v", err)
	}
	if got.UserID != u.ID {
		t.Errorf("user_id %q, want %s", got.UserID, u.ID)
	}
	if len(got.Scopes) != 3 {
		t.Errorf("scopes len %d, want 3", len(got.Scopes))
	}
}

func TestOIDCAccessTokenNotFound(t *testing.T) {
	store, ctx := setupOIDCIssuerStore(t)
	_, err := store.GetOIDCAccessTokenByHash(ctx, "nonexistent")
	if !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestDeleteExpiredOIDCAccessTokens(t *testing.T) {
	store, ctx := setupOIDCIssuerStore(t)
	u := createIssuerTestUser(t, store, ctx)

	// Create one expired and one valid token.
	_, _ = store.CreateOIDCAccessToken(ctx, auth.OIDCAccessToken{
		ID:        uuid.NewString(),
		TokenHash: "expired1",
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	_, _ = store.CreateOIDCAccessToken(ctx, auth.OIDCAccessToken{
		ID:        uuid.NewString(),
		TokenHash: "valid1",
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(time.Hour),
	})

	if err := store.DeleteExpiredOIDCAccessTokens(ctx); err != nil {
		t.Fatalf("DeleteExpiredOIDCAccessTokens: %v", err)
	}

	_, err := store.GetOIDCAccessTokenByHash(ctx, "expired1")
	if !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired token should be deleted, got %v", err)
	}
	_, err = store.GetOIDCAccessTokenByHash(ctx, "valid1")
	if err != nil {
		t.Errorf("valid token should still exist: %v", err)
	}
}
