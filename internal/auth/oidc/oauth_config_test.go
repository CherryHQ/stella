package oidc

import "testing"

// mapLookup adapts a map to the Lookup signature so tests exercise the same
// boundary-supplied path production uses (os.LookupEnv), without mutating the
// process environment.
func mapLookup(env map[string]string) Lookup {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestOAuthConfigsFeishuDefaults(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_PROVIDERS":                  "feishu",
		"AUTH_OAUTH_FEISHU_CLIENT_ID":           "cli_xxx",
		"AUTH_OAUTH_FEISHU_CLIENT_SECRET":       "secret",
		"AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS": "tenant-1",
	})

	configs, err := OAuthConfigs(lookup, "https://stella.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	cfg := configs[0]
	if cfg.ProviderName != "feishu" || cfg.Kind != "feishu" {
		t.Fatalf("provider = %q kind = %q", cfg.ProviderName, cfg.Kind)
	}
	if cfg.RedirectURL != "https://stella.example/auth/callback/feishu" {
		t.Fatalf("RedirectURL = %q", cfg.RedirectURL)
	}
	if cfg.TokenRequestStyle != "json" {
		t.Fatalf("TokenRequestStyle = %q", cfg.TokenRequestStyle)
	}
	if cfg.AuthURL == "" || cfg.TokenURL == "" || cfg.UserInfoURL == "" {
		t.Fatalf("missing Feishu defaults: %#v", cfg)
	}
}

func TestOAuthConfigsRequiresFeishuTenantAllowlist(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_PROVIDERS":                    "feishu",
		"AUTH_OAUTH_FEISHU_CLIENT_ID":             "cli_xxx",
		"AUTH_OAUTH_FEISHU_CLIENT_SECRET":         "secret",
		"AUTH_OAUTH_FEISHU_ALLOWED_EMAIL_DOMAINS": "example.com",
	})

	_, err := OAuthConfigs(lookup, "https://stella.example")
	if err == nil {
		t.Fatal("expected missing Feishu tenant allowlist error")
	}
}

func TestOAuthConfigsRequiresAllowRule(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_PROVIDERS":            "google",
		"AUTH_OAUTH_GOOGLE_CLIENT_ID":     "client",
		"AUTH_OAUTH_GOOGLE_CLIENT_SECRET": "secret",
	})

	_, err := OAuthConfigs(lookup, "https://stella.example")
	if err == nil {
		t.Fatal("expected missing allow rule error")
	}
}

func TestOAuthConfigsRejectsGoogleTenantAllowlist(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_PROVIDERS":                  "google",
		"AUTH_OAUTH_GOOGLE_CLIENT_ID":           "client",
		"AUTH_OAUTH_GOOGLE_CLIENT_SECRET":       "secret",
		"AUTH_OAUTH_GOOGLE_ALLOWED_TENANT_KEYS": "tenant-1",
	})

	_, err := OAuthConfigs(lookup, "https://stella.example")
	if err == nil {
		t.Fatal("expected unsupported Google tenant allowlist error")
	}
}

func TestOAuthConfigsRejectsReservedLocalProvider(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_PROVIDERS":                   "local",
		"AUTH_OAUTH_LOCAL_CLIENT_ID":             "client",
		"AUTH_OAUTH_LOCAL_CLIENT_SECRET":         "secret",
		"AUTH_OAUTH_LOCAL_AUTH_URL":              "https://idp.example/auth",
		"AUTH_OAUTH_LOCAL_TOKEN_URL":             "https://idp.example/token",
		"AUTH_OAUTH_LOCAL_USERINFO_URL":          "https://idp.example/userinfo",
		"AUTH_OAUTH_LOCAL_ALLOWED_EMAIL_DOMAINS": "example.com",
	})

	_, err := OAuthConfigs(lookup, "https://stella.example")
	if err == nil {
		t.Fatal("expected reserved local provider error")
	}
}

func TestOAuthConfigForProviderCanDisableEmailVerifiedRequirement(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_ACME_CLIENT_ID":              "client",
		"AUTH_OAUTH_ACME_CLIENT_SECRET":          "secret",
		"AUTH_OAUTH_ACME_AUTH_URL":               "https://idp.example/auth",
		"AUTH_OAUTH_ACME_TOKEN_URL":              "https://idp.example/token",
		"AUTH_OAUTH_ACME_USERINFO_URL":           "https://idp.example/userinfo",
		"AUTH_OAUTH_ACME_ALLOWED_EMAIL_DOMAINS":  "example.com",
		"AUTH_OAUTH_ACME_REQUIRE_EMAIL_VERIFIED": "false",
	})

	cfg, err := OAuthConfigForProvider(lookup, "acme", "https://stella.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequireEmailVerified {
		t.Fatal("expected RequireEmailVerified=false")
	}
}

func TestOAuthConfigForProviderCustomProvider(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_ACME_CLIENT_ID":             "client",
		"AUTH_OAUTH_ACME_CLIENT_SECRET":         "secret",
		"AUTH_OAUTH_ACME_AUTH_URL":              "https://idp.example/auth",
		"AUTH_OAUTH_ACME_TOKEN_URL":             "https://idp.example/token",
		"AUTH_OAUTH_ACME_USERINFO_URL":          "https://idp.example/userinfo",
		"AUTH_OAUTH_ACME_ALLOWED_EMAIL_DOMAINS": "example.com",
	})

	cfg, err := OAuthConfigForProvider(lookup, "acme", "https://stella.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderName != "acme" || cfg.Kind != "generic" {
		t.Fatalf("provider = %q kind = %q", cfg.ProviderName, cfg.Kind)
	}
	if cfg.AuthURL != "https://idp.example/auth" {
		t.Fatalf("AuthURL = %q", cfg.AuthURL)
	}
}
