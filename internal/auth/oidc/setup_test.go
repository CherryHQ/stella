package oidc

import "testing"

func TestLocalPasswordEnvPrefersNewName(t *testing.T) {
	t.Setenv("LOCAL_PASSWORD_ALLOW_REGISTRATION", "true")
	t.Setenv("LOCAL_OIDC_ALLOW_REGISTRATION", "false")

	if got := localPasswordEnv("ALLOW_REGISTRATION"); got != "true" {
		t.Fatalf("localPasswordEnv = %q, want new value", got)
	}
}

func TestLocalPasswordEnvFallsBackToLegacyLocalOIDCName(t *testing.T) {
	t.Setenv("LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS", "example.com")

	if got := localPasswordEnv("ALLOWED_EMAIL_DOMAINS"); got != "example.com" {
		t.Fatalf("localPasswordEnv = %q, want legacy value", got)
	}
}
