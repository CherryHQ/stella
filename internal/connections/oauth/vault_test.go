package oauth

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockVaultStore is a simple in-memory VaultStore for testing. Its mutex lets the
// concurrent-reload test write a winning bundle from one goroutine while GetToken
// reloads from another.
type mockVaultStore struct {
	mu   sync.RWMutex
	data map[string]string // keyed by "userID:name"
}

func newMockVaultStore() *mockVaultStore {
	return &mockVaultStore{data: make(map[string]string)}
}

func (m *mockVaultStore) key(userID string, name string) string {
	return userID + ":" + name
}

func (m *mockVaultStore) Set(_ context.Context, userID string, name string, plaintext string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(userID, name)] = plaintext
	return nil
}

func (m *mockVaultStore) Delete(_ context.Context, userID string, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.key(userID, name))
	return nil
}

func (m *mockVaultStore) Lookup(_ context.Context, userID string, name string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.data[m.key(userID, name)]
	return value, ok, nil
}

func TestSaveLoadOAuthBundle_RoundTrip(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "1"

	now := time.Now().UTC().Truncate(time.Second)
	bundle := OAuthBundle{
		Version:          1,
		ClientID:         "test_client_id",
		ClientSecret:     "test_client_secret",
		AccessToken:      "test_access_token",
		RefreshToken:     "test_refresh_token",
		AccessExpiresAt:  now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
	}

	if err := SaveOAuthBundle(ctx, vs, userID, VaultKeyGitHub, bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := LoadOAuthBundle(ctx, vs, userID, VaultKeyGitHub)
	if err != nil {
		t.Fatalf("LoadOAuthBundle: %v", err)
	}
	if got == nil {
		t.Fatal("LoadOAuthBundle: expected non-nil bundle")
	}
	if got.ClientID != bundle.ClientID {
		t.Errorf("ClientID = %q, want %q", got.ClientID, bundle.ClientID)
	}
	if got.AccessToken != bundle.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, bundle.AccessToken)
	}
	if got.RefreshToken != bundle.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, bundle.RefreshToken)
	}
	if !got.AccessExpiresAt.Equal(bundle.AccessExpiresAt) {
		t.Errorf("AccessExpiresAt = %v, want %v", got.AccessExpiresAt, bundle.AccessExpiresAt)
	}
	if got.Version != bundle.Version {
		t.Errorf("Version = %d, want %d", got.Version, bundle.Version)
	}
}

func TestSaveLoadOAuthBundle_GrantedScopeRoundTrip(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "gs"

	bundle := OAuthBundle{
		Version:      1,
		AccessToken:  "tok",
		GrantedScope: "im:message docs:read offline_access",
	}
	if err := SaveOAuthBundle(ctx, vs, userID, VaultKeyGitHub, bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}
	got, err := LoadOAuthBundle(ctx, vs, userID, VaultKeyGitHub)
	if err != nil {
		t.Fatalf("LoadOAuthBundle: %v", err)
	}
	if got.GrantedScope != bundle.GrantedScope {
		t.Errorf("GrantedScope = %q, want %q", got.GrantedScope, bundle.GrantedScope)
	}
}

func TestLoadOAuthBundle_OldFormatHasEmptyGrantedScope(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "old"

	// A pre-D3 bundle serialized without the granted_scope field.
	raw := `{"version":1,"client_id":"cid","access_token":"tok","access_expires_at":"2026-01-01T00:00:00Z"}`
	if err := vs.Set(ctx, userID, VaultKeyGitHub, raw); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := LoadOAuthBundle(ctx, vs, userID, VaultKeyGitHub)
	if err != nil {
		t.Fatalf("LoadOAuthBundle: %v", err)
	}
	if got == nil {
		t.Fatal("LoadOAuthBundle: expected non-nil bundle")
	}
	if got.GrantedScope != "" {
		t.Errorf("GrantedScope = %q, want empty (unknown) for old-format bundle", got.GrantedScope)
	}
}

func TestLoadOAuthBundle_AbsentKeyReturnsNil(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()

	got, err := LoadOAuthBundle(ctx, vs, "42", VaultKeyGitHub)
	if err != nil {
		t.Fatalf("LoadOAuthBundle: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("LoadOAuthBundle: expected nil for absent key, got %+v", got)
	}
}

func TestDeleteBundle(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "3"

	bundle := OAuthBundle{Version: 1, AccessToken: "ghp_todelete"}
	if err := SaveOAuthBundle(ctx, vs, userID, VaultKeyGitHub, bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	if err := DeleteBundle(ctx, vs, userID, VaultKeyGitHub); err != nil {
		t.Fatalf("DeleteBundle: %v", err)
	}

	got, err := LoadOAuthBundle(ctx, vs, userID, VaultKeyGitHub)
	if err != nil {
		t.Fatalf("LoadOAuthBundle after delete: %v", err)
	}
	if got != nil {
		t.Errorf("LoadOAuthBundle after delete: expected nil, got %+v", got)
	}
}
