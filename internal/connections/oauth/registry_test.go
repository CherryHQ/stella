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
	reg.Register(ProviderConfig{ID: "acme", VaultKey: "ACME_OAUTH"})
	reg.Register(ProviderConfig{ID: "empty"})

	cfg, ok := reg.Get("github")
	if !ok || cfg.ID != "github" {
		t.Fatalf("Get(github): got %v, ok=%v", cfg, ok)
	}
	_, ok = reg.Get("missing")
	if ok {
		t.Fatal("Get(missing) should return false")
	}

	vk, ok := reg.VaultKey("acme")
	if !ok || vk != "ACME_OAUTH" {
		t.Fatalf("VaultKey(acme) = %q, ok=%v, want ACME_OAUTH/true", vk, ok)
	}
	_, ok = reg.VaultKey("missing")
	if ok {
		t.Fatal("VaultKey(missing) should return false")
	}

	keys := reg.VaultKeys()
	if len(keys) != 2 || keys[0] != "ACME_OAUTH" || keys[1] != "GH_OAUTH" {
		t.Fatalf("VaultKeys() = %v, want [ACME_OAUTH GH_OAUTH]", keys)
	}

	ids := reg.IDs()
	if len(ids) != 3 || ids[0] != "acme" || ids[1] != "empty" || ids[2] != "github" {
		t.Fatalf("IDs() = %v, want [acme empty github]", ids)
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
			got := needsRefresh(&tt.bundle, defaultMinValidity)
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

	got, err := reg.GetToken(ctx, vs, "testprovider", userID, 0)
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

func TestGetToken_RefreshCapturesGrantedScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new_access_token",
			"token_type":   "Bearer",
			"expires_in":   7200,
			"scope":        "im:message docs:read",
		})
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
		AccessExpiresAt: time.Now().Add(5 * time.Minute),
		GrantedScope:    "im:message", // narrower than what the refresh reports
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "TEST_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "testprovider", userID, 0)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.GrantedScope != "im:message docs:read" {
		t.Errorf("GrantedScope = %q, want %q", got.GrantedScope, "im:message docs:read")
	}
}

func TestGetToken_RefreshPreservesGrantedScopeWhenAbsent(t *testing.T) {
	// A refresh response that omits scope must not wipe the prior grant.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new_access_token",
			"token_type":   "Bearer",
			"expires_in":   7200,
		})
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
		AccessToken:     "old_access_token",
		RefreshToken:    "old_refresh_token",
		AccessExpiresAt: time.Now().Add(5 * time.Minute),
		GrantedScope:    "im:message docs:read",
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "TEST_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "testprovider", userID, 0)
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.GrantedScope != "im:message docs:read" {
		t.Errorf("GrantedScope = %q, want preserved %q", got.GrantedScope, "im:message docs:read")
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

	got, err := reg.GetToken(ctx, vs, "freshprovider", userID, 0)
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

func TestGetToken_RefreshFailureBelowMinValidityErrors(t *testing.T) {
	// A refresh that fails must not fall back to a token that can no longer cover
	// the caller's minimum validity — the tool would get a credential that dies
	// mid-turn. The caller gets an actionable error instead (#722).
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
		AccessExpiresAt: time.Now().Add(2 * time.Minute), // below the 10-min floor
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "FAIL_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "failprovider", userID, 0)
	if err == nil {
		t.Fatalf("GetToken should error when refresh fails and the token is below min-validity; got %+v", got)
	}
}

func TestGetToken_ExpiredTokenNeverReturned(t *testing.T) {
	// An already-expired token with no way to refresh must surface as an error,
	// never be returned as a usable credential (#722).
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "9"

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{ID: "expprovider", VaultKey: "EXP_OAUTH"})

	bundle := OAuthBundle{
		Version:         1,
		AccessToken:     "dead_token",
		AccessExpiresAt: time.Now().Add(-1 * time.Minute), // already expired
		// No refresh token: nothing to trade for a new access token.
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "EXP_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "expprovider", userID, 0)
	if err == nil {
		t.Fatalf("GetToken should error for an expired token; got %+v", got)
	}
}

func TestGetToken_ConcurrentReplicaReloadWins(t *testing.T) {
	// When a refresh fails because another replica already rotated the refresh
	// token, GetToken must consume the fresh bundle that replica persisted rather
	// than error or serve the stale token (#722). The token endpoint stands in for
	// the concurrent winner: it persists a fresh bundle to the shared vault, then
	// rejects our reused refresh token.
	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "7"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return the loser's invalid_grant before the winner persists, matching the
		// real ordering that makes a single immediate reload insufficient.
		go func() {
			time.Sleep(40 * time.Millisecond)
			winner := OAuthBundle{
				Version:         1,
				ClientID:        "cid",
				ClientSecret:    "csecret",
				AccessToken:     "winner_access_token",
				RefreshToken:    "rotated_refresh",
				AccessExpiresAt: time.Now().Add(2 * time.Hour),
			}
			_ = SaveOAuthBundle(ctx, vs, userID, "RACE_OAUTH", winner)
		}()
		http.Error(w, "refresh_token_reused", http.StatusBadRequest)
	}))
	defer srv.Close()

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "raceprovider",
		VaultKey: "RACE_OAUTH",
		Flows: []ProviderFlowConfig{
			{Type: "authorization_code", TokenURL: srv.URL + "/token"},
		},
	})

	stale := OAuthBundle{
		Version:         1,
		ClientID:        "cid",
		ClientSecret:    "csecret",
		AccessToken:     "stale_access_token",
		RefreshToken:    "shared_refresh",
		AccessExpiresAt: time.Now().Add(1 * time.Minute), // below floor -> triggers refresh
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "RACE_OAUTH", stale); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	got, err := reg.GetToken(ctx, vs, "raceprovider", userID, 0)
	if err != nil {
		t.Fatalf("GetToken should consume the concurrently-refreshed bundle: %v", err)
	}
	if got.AccessToken != "winner_access_token" {
		t.Errorf("AccessToken = %q, want winner_access_token (reloaded from vault)", got.AccessToken)
	}
}

func TestGetToken_MinValidityCoversChatTimeout(t *testing.T) {
	// A token comfortably outside the fixed 10-min window must still be refreshed
	// when the caller demands a longer validity than a single chat turn (#722):
	// the old fixed window could not guarantee a token outlived the turn.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "renewed_for_turn",
			"token_type":    "Bearer",
			"expires_in":    7200,
			"refresh_token": "next_refresh",
		})
	}))
	defer srv.Close()

	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "8"

	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "turnprovider",
		VaultKey: "TURN_OAUTH",
		Flows: []ProviderFlowConfig{
			{Type: "authorization_code", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams},
		},
	})

	bundle := OAuthBundle{
		Version:         1,
		ClientID:        "cid",
		ClientSecret:    "csecret",
		AccessToken:     "valid_for_20m",
		RefreshToken:    "ref",
		AccessExpiresAt: time.Now().Add(20 * time.Minute), // outside 10m, inside a 35m turn floor
	}
	if err := SaveOAuthBundle(ctx, vs, userID, "TURN_OAUTH", bundle); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	// 10-min default leaves the 20-min token untouched.
	got, err := reg.GetToken(ctx, vs, "turnprovider", userID, 0)
	if err != nil {
		t.Fatalf("GetToken (default validity): %v", err)
	}
	if called || got.AccessToken != "valid_for_20m" {
		t.Fatalf("default validity should not refresh a 20-min token; called=%v token=%q", called, got.AccessToken)
	}

	// A 35-min floor (30m turn + 5m margin) forces a refresh.
	got, err = reg.GetToken(ctx, vs, "turnprovider", userID, 35*time.Minute)
	if err != nil {
		t.Fatalf("GetToken (turn validity): %v", err)
	}
	if !called || got.AccessToken != "renewed_for_turn" {
		t.Fatalf("turn validity should refresh; called=%v token=%q", called, got.AccessToken)
	}
}

func TestGetToken_RejectsRefreshedTokenBelowMinValidity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "still_too_short",
			"token_type":    "Bearer",
			"expires_in":    300,
			"refresh_token": "next_refresh",
		})
	}))
	defer srv.Close()

	vs := newMockVaultStore()
	ctx := context.Background()
	userID := "10"
	reg := NewProviderRegistry()
	reg.Register(ProviderConfig{
		ID:       "shortprovider",
		VaultKey: "SHORT_OAUTH",
		Flows: []ProviderFlowConfig{{
			Type: "authorization_code", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams,
		}},
	})
	if err := SaveOAuthBundle(ctx, vs, userID, "SHORT_OAUTH", OAuthBundle{
		Version:         1,
		ClientID:        "cid",
		ClientSecret:    "secret",
		AccessToken:     "expiring",
		RefreshToken:    "refresh",
		AccessExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	if got, err := reg.GetToken(ctx, vs, "shortprovider", userID, 35*time.Minute); err == nil {
		t.Fatalf("GetToken should reject a refreshed token below min-validity; got %+v", got)
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

	got, err := reg.GetToken(ctx, vs, "github", userID, 0)
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
