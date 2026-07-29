package sandbox

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// refreshSession is a fake session whose env can be refreshed in place.
type refreshSession struct {
	pkgsandbox.Session
	env       map[string]string
	onRefresh func()
}

func (s *refreshSession) Policy() pkgsandbox.Policy { return pkgsandbox.Policy{Env: s.env} }

func (s *refreshSession) RefreshEnv(updates map[string]string) {
	if s.onRefresh != nil {
		s.onRefresh()
	}
	maps.Copy(s.env, updates)
}

// staticSession is a fake session that cannot refresh its env (no EnvRefresher).
type staticSession struct {
	pkgsandbox.Session
	env map[string]string
}

func (s *staticSession) Policy() pkgsandbox.Policy { return pkgsandbox.Policy{Env: s.env} }

// acmeSpecs is the standard set of oauth-sourced env specs for the test provider.
func acmeSpecs() []pkgplugins.SessionEnvSpec {
	return []pkgplugins.SessionEnvSpec{
		{EnvVar: "ACME_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "acme"},
	}
}

// oauthRefreshConfig wires a TokenManager over store with a single test provider
// whose token endpoint is tokenURL (empty when refresh is not expected). By
// default it binds every oauth-sourced spec as if it had been injected from
// OAuth at session creation; tests that need an unbound (vault-overridden) var
// clear the binding afterwards.
func oauthRefreshConfig(t *testing.T, store oauth.VaultStore, tokenURL string, specs []pkgplugins.SessionEnvSpec) Config {
	t.Helper()
	registry := oauth.NewProviderRegistry()
	pc := oauth.ProviderConfig{ID: "acme", VaultKey: "ACME_OAUTH"}
	if tokenURL != "" {
		pc.Flows = []oauth.ProviderFlowConfig{{Type: "authorization_code", TokenURL: tokenURL}}
	}
	registry.Register(pc)
	tm := oauth.NewTokenManager(store)
	tm.SetRegistry(registry)

	bindings := NewOAuthEnvBindings()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		if strings.HasPrefix(string(s.Source), "oauth.") {
			names = append(names, s.EnvVar)
		}
	}
	bindings.Set(names)

	return Config{
		UserID:           "u1",
		AgentID:          "a1",
		TokenManager:     tm,
		SessionEnvSpecs:  specs,
		OAuthEnvBindings: bindings,
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

	if err := oauth.SaveOAuthBundle(ctx, store, "u1", "ACME_OAUTH", oauth.OAuthBundle{
		Version:         1,
		ClientID:        "app",
		ClientSecret:    "secret",
		AccessToken:     "about_to_expire",
		RefreshToken:    "shared_refresh",
		AccessExpiresAt: time.Now().Add(10 * time.Minute), // below the 35m turn floor
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"ACME_TOKEN": "about_to_expire"}}
	cfg := oauthRefreshConfig(t, store, srv.URL+"/token", acmeSpecs())

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["ACME_TOKEN"]; got != "rotated_access_token" {
		t.Fatalf("ACME_TOKEN = %q, want rotated_access_token", got)
	}
}

// TestRefreshSessionEnvUpdatesRedaction asserts a rotated secret value is added
// to SessionSecretValues while the value captured at session start is retained,
// so tool output redaction covers both the old and new access token.
func TestRefreshSessionEnvUpdatesRedaction(t *testing.T) {
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

	if err := oauth.SaveOAuthBundle(ctx, store, "u1", "ACME_OAUTH", oauth.OAuthBundle{
		Version:         1,
		ClientID:        "app",
		ClientSecret:    "secret",
		AccessToken:     "about_to_expire",
		RefreshToken:    "shared_refresh",
		AccessExpiresAt: time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	secrets := NewSessionSecretValues()
	secrets.Set([]string{"about_to_expire"}) // captured at session start

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"ACME_TOKEN": "about_to_expire"}}
	sess.onRefresh = func() {
		if !slices.Contains(secrets.Values(), "rotated_access_token") {
			t.Fatal("rotated token became executable before redaction was updated")
		}
	}
	cfg := oauthRefreshConfig(t, store, srv.URL+"/token", acmeSpecs())
	cfg.SessionSecretValues = secrets

	RefreshSessionEnv(ctx, sess, cfg)

	values := secrets.Values()
	if !slices.Contains(values, "about_to_expire") {
		t.Errorf("redaction must retain the original secret; values = %v", values)
	}
	if !slices.Contains(values, "rotated_access_token") {
		t.Errorf("redaction must add the rotated secret; values = %v", values)
	}
}

// TestRefreshSessionEnvSkipsUnboundVar asserts a var that was NOT injected from
// OAuth at creation (e.g. an explicit vault override of the same name) is left
// untouched even though a fresh bundle exists — only recorded OAuth-origin vars
// are live-refreshed.
func TestRefreshSessionEnvSkipsUnboundVar(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()

	if err := oauth.SaveOAuthBundle(ctx, store, "u1", "ACME_OAUTH", oauth.OAuthBundle{
		Version:         1,
		AccessToken:     "vault_oauth_token",
		AccessExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"ACME_TOKEN": "explicit_override"}}
	cfg := oauthRefreshConfig(t, store, "", acmeSpecs())
	// Simulate injectSessionEnv skipping OAuth because the vault explicitly
	// provided ACME_TOKEN: the var is never recorded as an OAuth binding.
	cfg.OAuthEnvBindings.Set(nil)

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["ACME_TOKEN"]; got != "explicit_override" {
		t.Fatalf("unbound (vault-overridden) var must not be refreshed; ACME_TOKEN = %q", got)
	}
}

// TestRefreshSessionEnvGroupNoOp asserts a group session never touches the human
// OAuth vault: the env is left untouched even though a fresh bundle exists.
func TestRefreshSessionEnvGroupNoOp(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()

	if err := oauth.SaveOAuthBundle(ctx, store, "u1", "ACME_OAUTH", oauth.OAuthBundle{
		Version:         1,
		AccessToken:     "vault_token",
		AccessExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"ACME_TOKEN": "stale"}}
	cfg := oauthRefreshConfig(t, store, "", acmeSpecs())
	cfg.GroupID = "g1"

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["ACME_TOKEN"]; got != "stale" {
		t.Fatalf("group session must not load the human vault; ACME_TOKEN = %q, want stale", got)
	}
}

// TestRefreshSessionEnvPreservesOldEnvOnFailure asserts a resolution failure
// (provider not connected) leaves the previous env value intact rather than
// clearing it.
func TestRefreshSessionEnvPreservesOldEnvOnFailure(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore() // no bundle saved -> "has not connected"

	sess := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{"ACME_TOKEN": "still_working"}}
	cfg := oauthRefreshConfig(t, store, "", acmeSpecs())

	RefreshSessionEnv(ctx, sess, cfg)

	if got := sess.env["ACME_TOKEN"]; got != "still_working" {
		t.Fatalf("failed refresh must preserve old env; ACME_TOKEN = %q, want still_working", got)
	}
}

// TestRefreshSessionEnvNoOpWithoutRefresher asserts a session that cannot refresh
// its env is a safe no-op (no panic).
func TestRefreshSessionEnvNoOpWithoutRefresher(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()
	if err := oauth.SaveOAuthBundle(ctx, store, "u1", "ACME_OAUTH", oauth.OAuthBundle{
		Version:         1,
		AccessToken:     "vault_token",
		AccessExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}

	sess := &staticSession{Session: pkgsandbox.NopSession(), env: map[string]string{"ACME_TOKEN": "stale"}}
	cfg := oauthRefreshConfig(t, store, "", acmeSpecs())

	RefreshSessionEnv(ctx, sess, cfg) // must not panic

	if got := sess.env["ACME_TOKEN"]; got != "stale" {
		t.Fatalf("static session env must be untouched; ACME_TOKEN = %q", got)
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
