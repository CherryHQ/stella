package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// larkSpecs is the standard set of oauth-sourced env specs for the lark provider.
func larkSpecs() []pkgplugins.SessionEnvSpec {
	return []pkgplugins.SessionEnvSpec{
		{EnvVar: "LARK_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "lark"},
	}
}

// oauthRefreshConfig wires a TokenManager over store with a single lark provider
// whose token endpoint is tokenURL (empty when refresh is not expected).
func oauthRefreshConfig(t *testing.T, store oauth.VaultStore, tokenURL string, specs []pkgplugins.SessionEnvSpec) Config {
	t.Helper()
	registry := oauth.NewProviderRegistry()
	pc := oauth.ProviderConfig{ID: "lark", VaultKey: oauth.VaultKeyLark}
	if tokenURL != "" {
		pc.Flows = []oauth.ProviderFlowConfig{{Type: "authorization_code", TokenURL: tokenURL}}
	}
	registry.Register(pc)
	tm := oauth.NewTokenManager(store)
	tm.SetRegistry(registry)
	return Config{
		UserID:          "u1",
		AgentID:         "a1",
		TokenManager:    tm,
		SessionEnvSpecs: specs,
	}
}

// TestRefreshSessionEnvLiveRefresh drives the full live path: a near-expiry
// bundle is refreshed through the TokenManager and the new access token lands in
// the sandbox env in place of the stale one.
func TestRefreshSessionEnvLiveRefresh(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "rotated_access_token",
			"token_type":    "Bearer",
			"expires_in":    7200,
			"refresh_token": "next_refresh",
		})
	}))
	defer srv.Close()

	if err := oauth.SaveOAuthBundle(ctx, store, "u1", oauth.VaultKeyLark, oauth.OAuthBundle{
		Version:         1,
		ClientID:        "app",
		ClientSecret:    "secret",
		AccessToken:     "about_to_expire",
		RefreshToken:    "shared_refresh",
		AccessExpiresAt: time.Now().Add(10 * time.Minute), // below the 35m turn floor
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"LARK_TOKEN": "about_to_expire"}}
	cfg := oauthRefreshConfig(t, store, srv.URL+"/token", larkSpecs())

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["LARK_TOKEN"]; got != "rotated_access_token" {
		t.Fatalf("LARK_TOKEN = %q, want rotated_access_token", got)
	}
}

// TestRefreshSessionEnvGroupNoOp asserts a group session never touches the human
// OAuth vault: the env is left untouched even though a fresh bundle exists.
func TestRefreshSessionEnvGroupNoOp(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()

	if err := oauth.SaveOAuthBundle(ctx, store, "u1", oauth.VaultKeyLark, oauth.OAuthBundle{
		Version:         1,
		AccessToken:     "vault_token",
		AccessExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"LARK_TOKEN": "stale"}}
	cfg := oauthRefreshConfig(t, store, "", larkSpecs())
	cfg.GroupID = "g1"

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["LARK_TOKEN"]; got != "stale" {
		t.Fatalf("group session must not load the human vault; LARK_TOKEN = %q, want stale", got)
	}
}

// TestRefreshSessionEnvPreservesOldEnvOnFailure asserts a resolution failure
// (provider not connected) leaves the previous env value intact rather than
// clearing it.
func TestRefreshSessionEnvPreservesOldEnvOnFailure(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore() // no bundle saved -> "has not connected"

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"LARK_TOKEN": "still_working"}}
	cfg := oauthRefreshConfig(t, store, "", larkSpecs())

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["LARK_TOKEN"]; got != "still_working" {
		t.Fatalf("failed refresh must preserve old env; LARK_TOKEN = %q, want still_working", got)
	}
}

// TestRefreshSessionEnvNoOpWithoutRefresher asserts a session that cannot refresh
// its env is a safe no-op (no panic).
func TestRefreshSessionEnvNoOpWithoutRefresher(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()
	if err := oauth.SaveOAuthBundle(ctx, store, "u1", oauth.VaultKeyLark, oauth.OAuthBundle{
		Version:         1,
		AccessToken:     "vault_token",
		AccessExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &staticSession{Session: pkgsandbox.NopSession(), env: map[string]string{"LARK_TOKEN": "stale"}}
	cfg := oauthRefreshConfig(t, store, "", larkSpecs())

	RefreshSessionEnv(ctx, sess, cfg) // must not panic

	if got := sess.env["LARK_TOKEN"]; got != "stale" {
		t.Fatalf("static session env must be untouched; LARK_TOKEN = %q", got)
	}
}

// TestRefreshSessionEnvNoOpWithoutOAuthSpecs asserts static-only specs never
// trigger an env update.
func TestRefreshSessionEnvNoOpWithoutOAuthSpecs(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"FOO": "bar"}}
	cfg := oauthRefreshConfig(t, store, "", []pkgplugins.SessionEnvSpec{
		{EnvVar: "FOO", Source: pkgplugins.SessionEnvSourceStatic, Value: "changed"},
	})

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["FOO"]; got != "bar" {
		t.Fatalf("static specs must not be refreshed; FOO = %q, want bar", got)
	}
}
