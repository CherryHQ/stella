package oauthcli

import (
	"context"
	"testing"
	"time"
)

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
