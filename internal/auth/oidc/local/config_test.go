package local

import "testing"

func TestAllowRegistrationFromEnv(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"true", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"off", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		if got := AllowRegistrationFromEnv(tt.value); got != tt.want {
			t.Fatalf("AllowRegistrationFromEnv(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestIsEmailAllowed(t *testing.T) {
	cfg := Config{AllowedEmailDomains: []string{"example.com"}}
	if !cfg.IsEmailAllowed("alice@example.com") {
		t.Fatal("expected exact domain to be allowed")
	}
	if !cfg.IsEmailAllowed("alice@team.example.com") {
		t.Fatal("expected subdomain to be allowed")
	}
	if cfg.IsEmailAllowed("alice@evil-example.com") {
		t.Fatal("expected suffix-like unrelated domain to be rejected")
	}
}
