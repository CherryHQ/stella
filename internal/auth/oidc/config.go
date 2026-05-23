package oidc

import (
	"errors"
	"os"
	"strings"
)

// Config holds the configuration for a generic OIDC provider.
// All values are loaded from environment variables via ConfigFromEnv.
type Config struct {
	ProviderName string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	OrgIDClaim   string // optional; e.g. "urn:zitadel:iam:org:id"
	OrgNameClaim string // optional; e.g. "urn:zitadel:iam:org:name"
}

// ConfigFromEnv reads OIDC configuration from environment variables.
//
//   - OIDC_PROVIDER_NAME (required)
//   - OIDC_ISSUER_URL    (required)
//   - OIDC_CLIENT_ID     (required)
//   - OIDC_CLIENT_SECRET (required)
//   - OIDC_REDIRECT_URL  (required)
//   - OIDC_SCOPES        (optional, comma-separated; default: openid email profile)
//   - OIDC_ORG_ID_CLAIM  (optional)
//   - OIDC_ORG_NAME_CLAIM (optional)
func ConfigFromEnv() (*Config, error) {
	cfg := &Config{
		ProviderName: os.Getenv("OIDC_PROVIDER_NAME"),
		IssuerURL:    os.Getenv("OIDC_ISSUER_URL"),
		ClientID:     os.Getenv("OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("OIDC_REDIRECT_URL"),
		OrgIDClaim:   os.Getenv("OIDC_ORG_ID_CLAIM"),
		OrgNameClaim: os.Getenv("OIDC_ORG_NAME_CLAIM"),
	}

	if raw := os.Getenv("OIDC_SCOPES"); raw != "" {
		cfg.Scopes = splitTrimmed(raw)
	} else {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}

	return cfg, cfg.Validate()
}

// Validate returns an error if any required field is missing.
func (c *Config) Validate() error {
	var errs []string
	if c.ProviderName == "" {
		errs = append(errs, "OIDC_PROVIDER_NAME is required")
	}
	if c.IssuerURL == "" {
		errs = append(errs, "OIDC_ISSUER_URL is required")
	}
	if c.ClientID == "" {
		errs = append(errs, "OIDC_CLIENT_ID is required")
	}
	if c.ClientSecret == "" {
		errs = append(errs, "OIDC_CLIENT_SECRET is required")
	}
	if c.RedirectURL == "" {
		errs = append(errs, "OIDC_REDIRECT_URL is required")
	}
	if len(errs) > 0 {
		return errors.New("oidc config: " + strings.Join(errs, "; "))
	}
	return nil
}

func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
