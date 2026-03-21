package feishutool

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
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

func TestNewSQLiteTokenStore(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "test-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}
}

func TestTokenStoreSetAndGet(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "test-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	token := Token{
		AccessToken:      "access-token-123",
		RefreshToken:     "refresh-token-456",
		ExpiresAt:        now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
	}

	if err := store.Set(ctx, "ou_test123", token); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(ctx, "ou_test123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.AccessToken != token.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, token.AccessToken)
	}
	if got.RefreshToken != token.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, token.RefreshToken)
	}
	// Compare with 1-second precision due to RFC3339 formatting.
	if got.ExpiresAt.Unix() != token.ExpiresAt.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, token.ExpiresAt)
	}
	if got.RefreshExpiresAt.Unix() != token.RefreshExpiresAt.Unix() {
		t.Errorf("RefreshExpiresAt = %v, want %v", got.RefreshExpiresAt, token.RefreshExpiresAt)
	}
}

func TestTokenStoreUpsert(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "test-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	token1 := Token{
		AccessToken:      "access-1",
		RefreshToken:     "refresh-1",
		ExpiresAt:        now.Add(1 * time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.Set(ctx, "ou_user", token1); err != nil {
		t.Fatalf("Set(1): %v", err)
	}

	token2 := Token{
		AccessToken:      "access-2",
		RefreshToken:     "refresh-2",
		ExpiresAt:        now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(48 * time.Hour),
	}
	if err := store.Set(ctx, "ou_user", token2); err != nil {
		t.Fatalf("Set(2): %v", err)
	}

	got, err := store.Get(ctx, "ou_user")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken != "access-2" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "access-2")
	}
}

func TestTokenStoreDelete(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "test-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}

	ctx := context.Background()
	token := Token{
		AccessToken:      "access",
		RefreshToken:     "refresh",
		ExpiresAt:        time.Now().Add(time.Hour),
		RefreshExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := store.Set(ctx, "ou_del", token); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.Delete(ctx, "ou_del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Get(ctx, "ou_del")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestTokenStoreGetNotFound(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "test-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}

	_, err = store.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent token")
	}
}

func TestTokenExpiry(t *testing.T) {
	now := time.Now()

	// Valid token.
	valid := Token{
		AccessToken:      "a",
		RefreshToken:     "r",
		ExpiresAt:        now.Add(time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}
	if valid.IsExpired() {
		t.Error("valid token should not be expired")
	}
	if valid.IsRefreshExpired() {
		t.Error("valid token refresh should not be expired")
	}

	// Expired access token, valid refresh.
	expiredAccess := Token{
		AccessToken:      "a",
		RefreshToken:     "r",
		ExpiresAt:        now.Add(-time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}
	if !expiredAccess.IsExpired() {
		t.Error("expired access token should be expired")
	}
	if expiredAccess.IsRefreshExpired() {
		t.Error("refresh token should not be expired")
	}

	// Both expired.
	allExpired := Token{
		AccessToken:      "a",
		RefreshToken:     "r",
		ExpiresAt:        now.Add(-2 * time.Hour),
		RefreshExpiresAt: now.Add(-time.Hour),
	}
	if !allExpired.IsExpired() {
		t.Error("access token should be expired")
	}
	if !allExpired.IsRefreshExpired() {
		t.Error("refresh token should be expired")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "my-app-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}

	testCases := []string{
		"simple-token",
		"",
		"a",
		"very-long-token-" + string(make([]byte, 1000)),
		"unicode-日本語-中文-한국어",
		"special chars: !@#$%^&*()",
	}

	for _, tc := range testCases {
		encrypted, err := store.encrypt(tc)
		if err != nil {
			t.Errorf("encrypt(%q): %v", tc, err)
			continue
		}
		decrypted, err := store.decrypt(encrypted)
		if err != nil {
			t.Errorf("decrypt(%q): %v", tc, err)
			continue
		}
		if decrypted != tc {
			t.Errorf("round-trip failed: got %q, want %q", decrypted, tc)
		}
	}
}

func TestEncryptProducesDifferentCiphertext(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewSQLiteTokenStore(db, "test-secret")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}

	// Same plaintext should produce different ciphertext due to random nonce.
	c1, _ := store.encrypt("same-token")
	c2, _ := store.encrypt("same-token")
	if c1 == c2 {
		t.Error("same plaintext should produce different ciphertext (random nonce)")
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	db := setupTestDB(t)
	store1, _ := NewSQLiteTokenStore(db, "secret-1")
	store2, _ := NewSQLiteTokenStore(db, "secret-2")

	encrypted, err := store1.encrypt("my-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = store2.decrypt(encrypted)
	if err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	k1, err := deriveKey("test-secret")
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	k2, err := deriveKey("test-secret")
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}

	if string(k1) != string(k2) {
		t.Error("same input should produce same key")
	}

	k3, err := deriveKey("different-secret")
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	if string(k1) == string(k3) {
		t.Error("different input should produce different key")
	}
}

func TestNeedAuthError(t *testing.T) {
	err := &NeedAuthError{OpenID: "ou_test"}
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}
}
