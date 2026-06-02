package oidc

import "testing"

func TestOAuthConfigsFromEnvFeishuDefaults(t *testing.T) {
	t.Setenv("AUTH_OAUTH_PROVIDERS", "feishu")
	t.Setenv("AUTH_OAUTH_FEISHU_CLIENT_ID", "cli_xxx")
	t.Setenv("AUTH_OAUTH_FEISHU_CLIENT_SECRET", "secret")
	t.Setenv("AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS", "tenant-1")

	configs, err := OAuthConfigsFromEnv("https://stella.example")
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

func TestOAuthConfigsFromEnvRequiresFeishuTenantAllowlist(t *testing.T) {
	t.Setenv("AUTH_OAUTH_PROVIDERS", "feishu")
	t.Setenv("AUTH_OAUTH_FEISHU_CLIENT_ID", "cli_xxx")
	t.Setenv("AUTH_OAUTH_FEISHU_CLIENT_SECRET", "secret")
	t.Setenv("AUTH_OAUTH_FEISHU_ALLOWED_EMAIL_DOMAINS", "example.com")

	_, err := OAuthConfigsFromEnv("https://stella.example")
	if err == nil {
		t.Fatal("expected missing Feishu tenant allowlist error")
	}
}

func TestOAuthConfigsFromEnvRequiresAllowRule(t *testing.T) {
	t.Setenv("AUTH_OAUTH_PROVIDERS", "google")
	t.Setenv("AUTH_OAUTH_GOOGLE_CLIENT_ID", "client")
	t.Setenv("AUTH_OAUTH_GOOGLE_CLIENT_SECRET", "secret")

	_, err := OAuthConfigsFromEnv("https://stella.example")
	if err == nil {
		t.Fatal("expected missing allow rule error")
	}
}

func TestOAuthConfigFromEnvCustomProvider(t *testing.T) {
	t.Setenv("AUTH_OAUTH_ACME_CLIENT_ID", "client")
	t.Setenv("AUTH_OAUTH_ACME_CLIENT_SECRET", "secret")
	t.Setenv("AUTH_OAUTH_ACME_AUTH_URL", "https://idp.example/auth")
	t.Setenv("AUTH_OAUTH_ACME_TOKEN_URL", "https://idp.example/token")
	t.Setenv("AUTH_OAUTH_ACME_USERINFO_URL", "https://idp.example/userinfo")
	t.Setenv("AUTH_OAUTH_ACME_ALLOWED_EMAIL_DOMAINS", "example.com")

	cfg, err := OAuthConfigFromEnv("acme", "https://stella.example")
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
