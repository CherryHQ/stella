package sandbox

import (
	"testing"
	"time"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestOAuthSessionResourcesIsolateProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		scope    string
		clientID string
	}{
		{name: "missing required field", scope: "read"},
		{name: "missing required scope", scope: "write", clientID: "bad-new-client"},
		{name: "unknown granted scope", clientID: "bad-new-client"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			store := newStubOAuthVaultStore()
			registry := oauth.NewProviderRegistry()
			registry.Register(oauth.ProviderConfig{ID: "bad", VaultKey: "BAD_OAUTH"})
			registry.Register(oauth.ProviderConfig{ID: "good", VaultKey: "GOOD_OAUTH"})
			tokens := oauth.NewTokenManager(store)
			tokens.SetRegistry(registry)
			for key, bundle := range map[string]oauth.OAuthBundle{
				"BAD_OAUTH":  {Version: 1, AccessToken: "bad-new-token", ClientID: test.clientID, GrantedScope: test.scope, AccessExpiresAt: time.Now().UTC().Add(2 * time.Hour)},
				"GOOD_OAUTH": {Version: 1, AccessToken: "good-new-token", GrantedScope: "read", AccessExpiresAt: time.Now().UTC().Add(2 * time.Hour)},
			} {
				if err := oauth.SaveOAuthBundle(ctx, store, "user", key, bundle); err != nil {
					t.Fatal(err)
				}
			}
			cfg := Config{
				UserID: "user", TokenManager: tokens, OAuthEnvBindings: NewOAuthEnvBindings(), SessionSecretValues: NewSessionSecretValues(),
				SessionEnvSpecs: []pkgplugins.SessionEnvSpec{
					{PluginID: "combined", EnvVar: "BAD_TOKEN", Source: "oauth.access_token", OAuthProviderID: "bad", OAuthScopes: []string{"read"}, Required: true},
					{PluginID: "combined", EnvVar: "BAD_CLIENT", Source: "oauth.client_id", OAuthProviderID: "bad", OAuthScopes: []string{"read"}, Required: true},
					{PluginID: "combined", EnvVar: "GOOD_TOKEN", Source: "oauth.access_token", OAuthProviderID: "good", OAuthScopes: []string{"read"}, Required: true},
				},
			}
			env, secrets := map[string]string{}, map[string]string{}
			if err := injectSessionEnv(ctx, cfg, env, nil, secrets); err != nil {
				t.Fatalf("one provider failure stopped the package: %v", err)
			}
			if len(env) != 1 || env["GOOD_TOKEN"] != "good-new-token" || len(secrets) != 1 || secrets["GOOD_TOKEN"] != "good-new-token" {
				t.Fatal("initial injection exposed partial failing credentials or lost the healthy provider")
			}
			if !cfg.OAuthEnvBindings.Has("GOOD_TOKEN") || cfg.OAuthEnvBindings.Has("BAD_TOKEN") {
				t.Fatal("refresh bindings include credentials that were not injected")
			}
			// A cached runner previously had a complete credential set. A later
			// incomplete refresh must preserve that provider atomically.
			cfg.OAuthEnvBindings.Set([]string{"BAD_TOKEN", "BAD_CLIENT", "GOOD_TOKEN"})
			session := &refreshSession{Session: pkgsandbox.NopSession(), env: map[string]string{
				"BAD_TOKEN": "bad-old-token", "BAD_CLIENT": "bad-old-client", "GOOD_TOKEN": "good-old-token",
			}}
			session.onRefresh = func() {
				requireSessionSecretValues(t, cfg.SessionSecretValues.Values(), []string{"good-new-token"}, []string{"bad-new-token"})
			}
			RefreshSessionEnv(ctx, session, cfg)
			if session.env["GOOD_TOKEN"] != "good-new-token" || session.env["BAD_TOKEN"] != "bad-old-token" || session.env["BAD_CLIENT"] != "bad-old-client" {
				t.Fatal("refresh changed only part of a failed provider or did not rotate the healthy provider")
			}
		})
	}
}
