package feishutool

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	_ "modernc.org/sqlite"
)

func TestInvokeAsUserNoStore(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient) // no token store

	ctx := WithOpenID(context.Background(), "ou_test")
	var receivedToken string
	err := c.InvokeAsUser(ctx, false, func(ctx context.Context, token string) error {
		receivedToken = token
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeAsUser: %v", err)
	}
	if receivedToken != "" {
		t.Errorf("expected empty token (bot fallback), got %q", receivedToken)
	}
}

func TestInvokeAsUserNoStoreRequireAuth(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient) // no token store

	ctx := WithOpenID(context.Background(), "ou_test")
	err := c.InvokeAsUser(ctx, true, func(ctx context.Context, token string) error {
		t.Fatal("fn should not be called when requireAuth and no store")
		return nil
	})

	var needAuth *NeedAuthError
	if !errors.As(err, &needAuth) {
		t.Fatalf("expected NeedAuthError, got %v", err)
	}
}

func TestInvokeAsUserNoOpenID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient)

	ctx := context.Background() // no open_id in context
	var receivedToken string
	err := c.InvokeAsUser(ctx, false, func(ctx context.Context, token string) error {
		receivedToken = token
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeAsUser: %v", err)
	}
	if receivedToken != "" {
		t.Errorf("expected empty token, got %q", receivedToken)
	}
}

func TestInvokeAsUserWithValidToken(t *testing.T) {
	db := setupTokenTestDB(t)
	store, _ := NewSQLiteTokenStore(db, "secret")

	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient, WithTokenStore(store))

	ctx := WithOpenID(context.Background(), "ou_valid")
	token := Token{
		AccessToken:      "valid-access-token",
		RefreshToken:     "valid-refresh-token",
		ExpiresAt:        time.Now().Add(time.Hour),
		RefreshExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := store.Set(ctx, "ou_valid", token); err != nil {
		t.Fatalf("Set: %v", err)
	}

	var receivedToken string
	err := c.InvokeAsUser(ctx, false, func(ctx context.Context, tkn string) error {
		receivedToken = tkn
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeAsUser: %v", err)
	}
	if receivedToken != "valid-access-token" {
		t.Errorf("got token %q, want %q", receivedToken, "valid-access-token")
	}
}

func TestInvokeAsUserBothExpired(t *testing.T) {
	db := setupTokenTestDB(t)
	store, _ := NewSQLiteTokenStore(db, "secret")

	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient, WithTokenStore(store))

	ctx := WithOpenID(context.Background(), "ou_expired")
	token := Token{
		AccessToken:      "old-access",
		RefreshToken:     "old-refresh",
		ExpiresAt:        time.Now().Add(-2 * time.Hour),
		RefreshExpiresAt: time.Now().Add(-time.Hour),
	}
	if err := store.Set(ctx, "ou_expired", token); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// requireAuth=true: should return NeedAuthError when both tokens expired.
	err := c.InvokeAsUser(ctx, true, func(ctx context.Context, tkn string) error {
		t.Fatal("fn should not be called when both tokens expired and requireAuth=true")
		return nil
	})

	var needAuth *NeedAuthError
	if !errors.As(err, &needAuth) {
		t.Fatalf("expected NeedAuthError, got %v", err)
	}

	// requireAuth=false: should fall back to bot token.
	var receivedToken string
	err = c.InvokeAsUser(ctx, false, func(ctx context.Context, tkn string) error {
		receivedToken = tkn
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeAsUser: %v", err)
	}
	if receivedToken != "" {
		t.Errorf("expected empty token (bot fallback), got %q", receivedToken)
	}
}

func TestInvokeAsUserTokenNotFound(t *testing.T) {
	db := setupTokenTestDB(t)
	store, _ := NewSQLiteTokenStore(db, "secret")

	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient, WithTokenStore(store))

	ctx := WithOpenID(context.Background(), "ou_notoken")

	// requireAuth=false: falls back to bot token.
	var receivedToken string
	err := c.InvokeAsUser(ctx, false, func(ctx context.Context, tkn string) error {
		receivedToken = tkn
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeAsUser: %v", err)
	}
	if receivedToken != "" {
		t.Errorf("expected empty token, got %q", receivedToken)
	}
}

func TestClientTokenStore(t *testing.T) {
	db := setupTokenTestDB(t)
	store, _ := NewSQLiteTokenStore(db, "secret")

	larkClient := lark.NewClient("fake_id", "fake_secret")
	c := NewClient(larkClient, WithTokenStore(store))
	if c.TokenStore() != store {
		t.Error("TokenStore() should return the configured store")
	}

	c2 := NewClient(larkClient)
	if c2.TokenStore() != nil {
		t.Error("TokenStore() should return nil when not configured")
	}
}

func setupTokenTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE feishu_tokens (
		open_id            TEXT PRIMARY KEY,
		access_token       TEXT NOT NULL,
		refresh_token      TEXT NOT NULL,
		expires_at         TEXT NOT NULL,
		refresh_expires_at TEXT NOT NULL,
		created_at         TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}
