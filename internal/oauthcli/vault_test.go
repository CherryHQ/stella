package oauthcli

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// mockVaultStore is a simple in-memory VaultStore for testing.
type mockVaultStore struct {
	data map[string]string // keyed by "userID:name"
}

func newMockVaultStore() *mockVaultStore {
	return &mockVaultStore{data: make(map[string]string)}
}

func (m *mockVaultStore) key(userID int64, name string) string {
	return fmt.Sprintf("%d:%s", userID, name)
}

func (m *mockVaultStore) Set(_ context.Context, userID int64, name string, plaintext string) error {
	m.data[m.key(userID, name)] = plaintext
	return nil
}

func (m *mockVaultStore) Delete(_ context.Context, userID int64, name string) error {
	delete(m.data, m.key(userID, name))
	return nil
}

func (m *mockVaultStore) LoadEnv(_ context.Context, userID int64) (map[string]string, error) {
	out := make(map[string]string)
	prefix := fmt.Sprintf("%d:", userID)
	for k, v := range m.data {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			name := k[len(prefix):]
			out[name] = v
		}
	}
	return out, nil
}

func TestSaveLoadGHBundle_RoundTrip(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(1)

	expiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	bundle := GHOAuthBundle{
		Version:     1,
		AccessToken: "ghp_test_token",
		TokenType:   "bearer",
		Scope:       "repo,read:org",
		ExpiresAt:   &expiry,
	}

	if err := SaveGHBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveGHBundle: %v", err)
	}

	got, err := LoadGHBundle(ctx, vs, userID)
	if err != nil {
		t.Fatalf("LoadGHBundle: %v", err)
	}
	if got == nil {
		t.Fatal("LoadGHBundle: expected non-nil bundle")
	}
	if got.AccessToken != bundle.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, bundle.AccessToken)
	}
	if got.TokenType != bundle.TokenType {
		t.Errorf("TokenType = %q, want %q", got.TokenType, bundle.TokenType)
	}
	if got.Scope != bundle.Scope {
		t.Errorf("Scope = %q, want %q", got.Scope, bundle.Scope)
	}
	if got.Version != bundle.Version {
		t.Errorf("Version = %d, want %d", got.Version, bundle.Version)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(*bundle.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, bundle.ExpiresAt)
	}
}

func TestLoadGHBundle_AbsentKeyReturnsNil(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()

	got, err := LoadGHBundle(ctx, vs, 42)
	if err != nil {
		t.Fatalf("LoadGHBundle: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("LoadGHBundle: expected nil for absent key, got %+v", got)
	}
}

func TestSaveLoadLarkBundle_RoundTrip(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(2)

	now := time.Now().UTC().Truncate(time.Second)
	bundle := LarkOAuthBundle{
		Version:          1,
		AppID:            "cli_test_app_id",
		AppSecret:        "cli_test_app_secret",
		Brand:            "lark",
		AccessToken:      "u-test-access-token",
		RefreshToken:     "u-test-refresh-token",
		AccessExpiresAt:  now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
	}

	if err := SaveLarkBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveLarkBundle: %v", err)
	}

	got, err := LoadLarkBundle(ctx, vs, userID)
	if err != nil {
		t.Fatalf("LoadLarkBundle: %v", err)
	}
	if got == nil {
		t.Fatal("LoadLarkBundle: expected non-nil bundle")
	}
	if got.AppID != bundle.AppID {
		t.Errorf("AppID = %q, want %q", got.AppID, bundle.AppID)
	}
	if got.AppSecret != bundle.AppSecret {
		t.Errorf("AppSecret = %q, want %q", got.AppSecret, bundle.AppSecret)
	}
	if got.Brand != bundle.Brand {
		t.Errorf("Brand = %q, want %q", got.Brand, bundle.Brand)
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
}

func TestLoadLarkBundle_AbsentKeyReturnsNil(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()

	got, err := LoadLarkBundle(ctx, vs, 99)
	if err != nil {
		t.Fatalf("LoadLarkBundle: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("LoadLarkBundle: expected nil for absent key, got %+v", got)
	}
}

func TestDeleteBundle(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(3)

	bundle := GHOAuthBundle{Version: 1, AccessToken: "ghp_todelete"}
	if err := SaveGHBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveGHBundle: %v", err)
	}

	if err := DeleteBundle(ctx, vs, userID, VaultKeyGitHub); err != nil {
		t.Fatalf("DeleteBundle: %v", err)
	}

	got, err := LoadGHBundle(ctx, vs, userID)
	if err != nil {
		t.Fatalf("LoadGHBundle after delete: %v", err)
	}
	if got != nil {
		t.Errorf("LoadGHBundle after delete: expected nil, got %+v", got)
	}
}
