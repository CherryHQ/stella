package oidc

import "testing"

func TestLocalValuePrefersNewName(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"LOCAL_PASSWORD_ALLOW_REGISTRATION": "true",
		"LOCAL_OIDC_ALLOW_REGISTRATION":     "false",
	})

	if got := localValue(lookup, "ALLOW_REGISTRATION"); got != "true" {
		t.Fatalf("localValue = %q, want new value", got)
	}
}

func TestLocalValueFallsBackToLegacyLocalOIDCName(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS": "example.com",
	})

	if got := localValue(lookup, "ALLOWED_EMAIL_DOMAINS"); got != "example.com" {
		t.Fatalf("localValue = %q, want legacy value", got)
	}
}

func TestLoadLoginConfigParsesLocalAndOAuth(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"LOCAL_PASSWORD_ALLOW_REGISTRATION":     "true",
		"LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS":  "example.com",
		"AUTH_OAUTH_PROVIDERS":                  "feishu",
		"AUTH_OAUTH_FEISHU_CLIENT_ID":           "cli_xxx",
		"AUTH_OAUTH_FEISHU_CLIENT_SECRET":       "secret",
		"AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS": "tenant-1",
	})

	cfg, err := LoadLoginConfig(lookup, "https://stella.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Local.AllowRegistration != "true" {
		t.Fatalf("AllowRegistration = %q", cfg.Local.AllowRegistration)
	}
	if cfg.Local.AllowedEmailDomains != "example.com" {
		t.Fatalf("AllowedEmailDomains = %q", cfg.Local.AllowedEmailDomains)
	}
	if len(cfg.OAuth) != 1 || cfg.OAuth[0].ProviderName != "feishu" {
		t.Fatalf("OAuth = %#v", cfg.OAuth)
	}
}

func TestLoadLoginConfigFailsFastOnInvalidProvider(t *testing.T) {
	// A declared provider missing required fields must surface as an error at the
	// boundary rather than silently dropping the provider.
	lookup := mapLookup(map[string]string{
		"AUTH_OAUTH_PROVIDERS": "google",
	})

	if _, err := LoadLoginConfig(lookup, "https://stella.example"); err == nil {
		t.Fatal("expected invalid provider error")
	}
}
