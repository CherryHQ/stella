package oidc

import (
	"errors"
	"strings"

	"github.com/CherryHQ/stella/internal/platform/config"
)

// Config holds the configuration for a generic OIDC provider.
// All values are loaded from the server config snapshot via configFrom.
type Config struct {
	ProviderName string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

// configFrom builds the OIDC provider config from the static OIDC_* block of the
// server config snapshot. Consuming the snapshot (rather than re-reading the
// environment) is what keeps the mode decision, this config, and the gateway's
// dependent-feature probe reading one consistent generation.
//
//   - OIDC_PROVIDER_NAME (required)
//   - OIDC_ISSUER_URL    (required)
//   - OIDC_CLIENT_ID     (required)
//   - OIDC_CLIENT_SECRET (optional; empty = public client with PKCE only)
//   - OIDC_REDIRECT_URL  (required)
//   - OIDC_SCOPES        (optional, comma-separated; default: openid email profile)
func configFrom(c config.OIDCConfig) (*Config, error) {
	cfg := &Config{
		ProviderName: c.ProviderName,
		IssuerURL:    c.IssuerURL,
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.RedirectURL,
	}

	if raw := c.Scopes; raw != "" {
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
