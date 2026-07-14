package oauth

import (
	"context"
	"strings"
	"testing"
)

func TestNewTokenManager(t *testing.T) {
	vs := newMockVaultStore()
	tm := NewTokenManager(vs)
	if tm == nil {
		t.Fatal("NewTokenManager() returned nil")
	}
	if tm.vs != vs {
		t.Error("NewTokenManager() did not store vault store correctly")
	}
}

func TestGetOAuthToken_Success(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "1"

	registry := NewProviderRegistry()
	registry.Register(ProviderConfig{ID: "github", VaultKey: VaultKeyGitHub})
	tm := NewTokenManager(vs)
	tm.SetRegistry(registry)

	bundle := OAuthBundle{
		Version:     1,
		AccessToken: "ghp_test_token_12345",
	}
	if err := SaveOAuthBundle(ctx, vs, userID, VaultKeyGitHub, bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := tm.GetOAuthToken(ctx, "github", userID, 0)
	if err != nil {
		t.Fatalf("GetOAuthToken: %v", err)
	}
	if got.AccessToken != bundle.AccessToken {
		t.Errorf("GetOAuthToken AccessToken = %q, want %q", got.AccessToken, bundle.AccessToken)
	}
}

func TestGetOAuthToken_NoBundle(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "42"

	registry := NewProviderRegistry()
	registry.Register(ProviderConfig{ID: "github", VaultKey: VaultKeyGitHub})
	tm := NewTokenManager(vs)
	tm.SetRegistry(registry)

	_, err := tm.GetOAuthToken(ctx, "github", userID, 0)
	if err == nil {
		t.Fatal("GetOAuthToken expected error for missing bundle")
	}
	if !strings.Contains(err.Error(), "has not connected") {
		t.Errorf("error = %q, expected to contain 'has not connected'", err.Error())
	}
}

func TestGetOAuthToken_EmptyToken(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "1"

	registry := NewProviderRegistry()
	registry.Register(ProviderConfig{ID: "github", VaultKey: VaultKeyGitHub})
	tm := NewTokenManager(vs)
	tm.SetRegistry(registry)

	bundle := OAuthBundle{
		Version:     1,
		AccessToken: "",
	}
	if err := SaveOAuthBundle(ctx, vs, userID, VaultKeyGitHub, bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	_, err := tm.GetOAuthToken(ctx, "github", userID, 0)
	if err == nil {
		t.Fatal("GetOAuthToken expected error for empty token")
	}
	if !strings.Contains(err.Error(), "empty access token") {
		t.Errorf("error = %q, expected to contain 'empty access token'", err.Error())
	}
}

func TestGetOAuthToken_NoRegistry(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()

	_, err := NewTokenManager(vs).GetOAuthToken(ctx, "github", "1", 0)
	if err == nil {
		t.Fatal("GetOAuthToken expected error when registry not set")
	}
}
