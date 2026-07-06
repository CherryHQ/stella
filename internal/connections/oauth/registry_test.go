package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestProviderRegistry_GetAndVaultKeyAndIDs(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{ID: "github", VaultKey: "GH_OAUTH"})
	reg.Register(ProviderConfig{ID: "lark", VaultKey: "LARK_OAUTH"})

	cfg, ok := reg.Get("github")
	if !ok || cfg.ID != "github" {
		t.Fatalf("Get(github): got %v, ok=%v", cfg, ok)
	}
	_, ok = reg.Get("missing")
	if ok {
		t.Fatal("Get(missing) should return false")
	}

	vk, ok := reg.VaultKey("lark")
	if !ok || vk != "LARK_OAUTH" {
		t.Fatalf("VaultKey(lark) = %q, ok=%v, want LARK_OAUTH/true", vk, ok)
	}
	_, ok = reg.VaultKey("missing")
	if ok {
		t.Fatal("VaultKey(missing) should return false")
	}

	ids := reg.IDs()
	if len(ids) != 2 || ids[0] != "github" || ids[1] != "lark" {
		t.Fatalf("IDs() = %v, want [github lark]", ids)
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name   string
		bundle OAuthBundle
		want   bool
	}{
		{
			name:   "no refresh token",
			bundle: OAuthBundle{AccessToken: "tok", AccessExpiresAt: now.Add(5 * time.Minute)},
			want:   false,
		},
		{
			name:   "no expiry set",
			bundle: OAuthBundle{AccessToken: "tok", RefreshToken: "ref"},
			want:   false,
		},
		{
			name: "expires far away",
			bundle: OAuthBundle{
				AccessToken:     "tok",
				RefreshToken:    "ref",
				AccessExpiresAt: now.Add(30 * time.Minute),
			},
			want: false,
		},
		{
			name: "expires within window",
			bundle: OAuthBundle{
				AccessToken:     "tok",
				RefreshToken:    "ref",
				AccessExpiresAt: now.Add(5 * time.Minute),
			},
			want: true,
		},
		{
			name: "already expired",
			bundle: OAuthBundle{
				AccessToken:     "tok",
				RefreshToken:    "ref",
				AccessExpiresAt: now.Add(-1 * time.Minute),
			},
			want: true,
		},
		{
			name: "refresh token itself expired",
			bundle: OAuthBundle{
				AccessToken:      "tok",
				RefreshToken:     "ref",
				AccessExpiresAt:  now.Add(5 * time.Minute),
				RefreshExpiresAt: now.Add(-1 * time.Hour),
			},
			want: false,
		},
		{
			name: "refresh token valid, access expiring soon",
			bundle: OAuthBundle{
				AccessToken:      "tok",
				RefreshToken:     "ref",
				AccessExpiresAt:  now.Add(5 * time.Minute),
				RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsRefresh(&tt.bundle)
			if got != tt.want {
				t.Errorf("needsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetToken_RefreshesNearExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"access_token":  "new_access_token",
			"token_type":    "Bearer",
			"expires_in":    7200,
			"refresh_token": "new_refresh_token",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "1"

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "testprovider",
		VaultKey: "TEST_OAUTH",
		Flows: []ProviderFlowConfig{
			{Type: "authorization_code", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams},
		},
	})

	bundle := OAuthBundle{
		Version:         1,
		ClientID:        "client_id",
		ClientSecret:    "client_secret",
		AccessToken:     "old_access_token",
		RefreshToken:    "old_refresh_token",
		AccessExpiresAt: time.Now().Add(5 * time.Minute), // within 10-min window
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "TEST_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "testprovider", userID)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.AccessToken != "new_access_token" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "new_access_token")
	}
	if got.RefreshToken != "new_refresh_token" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "new_refresh_token")
	}

	// Verify the new bundle was persisted to vault.
	persisted, err := LoadOAuthBundle(ctx, vs, userID, "TEST_OAUTH")
	if err != nil || persisted == nil {
		t.Fatalf("LoadOAuthBundle after refresh: %v (bundle=%v)", err, persisted)
	}
	if persisted.AccessToken != "new_access_token" {
		t.Errorf("persisted AccessToken = %q, want %q", persisted.AccessToken, "new_access_token")
	}
}

func TestGetToken_SkipsRefreshWhenFreshToken(t *testing.T) {
	// Token endpoint should never be called for a fresh token.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "2"

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "freshprovider",
		VaultKey: "FRESH_OAUTH",
		Flows: []ProviderFlowConfig{
			{Type: "authorization_code", TokenURL: srv.URL + "/token"},
		},
	})

	bundle := OAuthBundle{
		Version:         1,
		ClientID:        "cid",
		ClientSecret:    "csecret",
		AccessToken:     "still_valid",
		RefreshToken:    "ref",
		AccessExpiresAt: time.Now().Add(30 * time.Minute), // well outside window
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "FRESH_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "freshprovider", userID)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.AccessToken != "still_valid" {
		t.Errorf("AccessToken = %q, want still_valid", got.AccessToken)
	}
	if called {
		t.Error("token endpoint was called for a fresh token; expected no refresh")
	}
}

func TestGetToken_RefreshFailureFallsBackToExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad_refresh_token", http.StatusBadRequest)
	}))
	defer srv.Close()

	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "3"

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "failprovider",
		VaultKey: "FAIL_OAUTH",
		Flows: []ProviderFlowConfig{
			{Type: "authorization_code", TokenURL: srv.URL + "/token"},
		},
	})

	bundle := OAuthBundle{
		Version:         1,
		ClientID:        "cid",
		ClientSecret:    "csecret",
		AccessToken:     "expiring_token",
		RefreshToken:    "bad_refresh",
		AccessExpiresAt: time.Now().Add(2 * time.Minute), // inside window
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "FAIL_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "failprovider", userID)
	if err != nil {
		t.Fatalf("GetToken should not error on refresh failure: %v", err)
	}
	// Falls back to original token.
	if got.AccessToken != "expiring_token" {
		t.Errorf("AccessToken = %q, want expiring_token", got.AccessToken)
	}
}

func TestGetToken_NoRefreshForTokenWithoutExpiry(t *testing.T) {
	// Simulates GitHub tokens: no AccessExpiresAt, so refresh must not be attempted.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "4"

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "github",
		VaultKey: VaultKeyGitHub,
		Flows: []ProviderFlowConfig{
			{Type: "device_code", TokenURL: srv.URL + "/token"},
		},
	})

	bundle := OAuthBundle{
		Version:     1,
		AccessToken: "ghp_longlivedtoken",
		// No RefreshToken, no AccessExpiresAt — typical GitHub OAuth app token.
	}
	if err := SaveOAuthBundle(ctx, vs, userID, VaultKeyGitHub, bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "github", userID)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.AccessToken != "ghp_longlivedtoken" {
		t.Errorf("AccessToken = %q, want ghp_longlivedtoken", got.AccessToken)
	}
	if called {
		t.Error("token endpoint called for GitHub token without expiry; expected no refresh")
	}
}
