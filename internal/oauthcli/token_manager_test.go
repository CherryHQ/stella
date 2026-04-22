package oauthcli

import (
	"context"
	"strings"
	"testing"
	"time"
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

func TestGetGHToken_Success(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(1)

	bundle := GHOAuthBundle{
		Version:     1,
		AccessToken: "ghp_test_token_12345",
		TokenType:   "bearer",
	}
	if err := SaveGHBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveGHBundle: %v", err)
	}

	token, err := NewTokenManager(vs).GetGHToken(ctx, userID)
	if err != nil {
		t.Fatalf("GetGHToken: %v", err)
	}
	if token != bundle.AccessToken {
		t.Errorf("GetGHToken = %q, want %q", token, bundle.AccessToken)
	}
}

func TestGetGHToken_NoBundle(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(42)

	_, err := NewTokenManager(vs).GetGHToken(ctx, userID)
	if err == nil {
		t.Fatal("GetGHToken expected error for missing bundle")
	}
	if !strings.Contains(err.Error(), "has not connected GitHub") {
		t.Errorf("error = %q, expected to contain 'has not connected GitHub'", err.Error())
	}
}

func TestGetGHToken_EmptyToken(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(1)

	bundle := GHOAuthBundle{
		Version:     1,
		AccessToken: "",
		TokenType:   "bearer",
	}
	if err := SaveGHBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveGHBundle: %v", err)
	}

	_, err := NewTokenManager(vs).GetGHToken(ctx, userID)
	if err == nil {
		t.Fatal("GetGHToken expected error for empty token")
	}
	if !strings.Contains(err.Error(), "empty access token") {
		t.Errorf("error = %q, expected to contain 'empty access token'", err.Error())
	}
}

func TestGetLarkRuntimeEnv_ExportsCurrentEnvNames(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(7)
	now := time.Now().UTC().Truncate(time.Second)

	bundle := LarkOAuthBundle{
		Version:          1,
		AppID:            "cli_test_app_id",
		AppSecret:        "cli_test_app_secret",
		Brand:            "feishu",
		AccessToken:      "u-test-access-token",
		RefreshToken:     "u-test-refresh-token",
		AccessExpiresAt:  now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
	}
	if err := SaveLarkBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveLarkBundle: %v", err)
	}

	env, err := NewTokenManager(vs).GetLarkRuntimeEnv(ctx, userID)
	if err != nil {
		t.Fatalf("GetLarkRuntimeEnv: %v", err)
	}

	checks := map[string]string{
		"LARKSUITE_CLI_USER_ACCESS_TOKEN": bundle.AccessToken,
		"LARKSUITE_CLI_APP_ID":            bundle.AppID,
		"LARKSUITE_CLI_BRAND":             bundle.Brand,
	}
	for key, want := range checks {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestGetLarkRuntimeEnv_NoBundle(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(99)

	_, err := NewTokenManager(vs).GetLarkRuntimeEnv(ctx, userID)
	if err == nil {
		t.Fatal("GetLarkRuntimeEnv expected error for missing bundle")
	}
	if !strings.Contains(err.Error(), "has not connected Lark/Feishu") {
		t.Errorf("error = %q, expected to contain 'has not connected Lark/Feishu'", err.Error())
	}
}

func TestGetLarkRuntimeEnv_RefreshTokenExpired(t *testing.T) {
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := int64(3)
	now := time.Now().UTC().Truncate(time.Second)

	// Refresh token already expired
	bundle := LarkOAuthBundle{
		Version:          1,
		AppID:            "test_app_id",
		AppSecret:        "test_app_secret",
		Brand:            "lark",
		AccessToken:      "access-token",
		RefreshToken:     "refresh-token",
		AccessExpiresAt:  now.Add(-1 * time.Hour),  // Expired
		RefreshExpiresAt: now.Add(-1 * time.Hour), // Also expired
	}
	if err := SaveLarkBundle(ctx, vs, userID, bundle); err != nil {
		t.Fatalf("SaveLarkBundle: %v", err)
	}

	_, err := NewTokenManager(vs).GetLarkRuntimeEnv(ctx, userID)
	if err == nil {
		t.Fatal("GetLarkRuntimeEnv expected error for expired refresh token")
	}
	if !strings.Contains(err.Error(), "refresh token expired") {
		t.Errorf("error = %q, expected to contain 'refresh token expired'", err.Error())
	}
}
