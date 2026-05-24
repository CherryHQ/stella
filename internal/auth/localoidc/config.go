// Package localoidc implements a minimal built-in OIDC issuer for Stella.
// It is designed for development and single-user/self-contained deployments.
// The issuer supports authorization-code + PKCE only. All configuration is
// loaded from environment variables at startup; no UI/API runtime mutation.
package localoidc

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Config holds the configuration for the built-in local OIDC issuer.
// All values are loaded from environment variables; see ConfigFromEnv.
type Config struct {
	// IssuerURL is the base URL for the local OIDC issuer, e.g.
	// "http://localhost:25678/oidc/local". It appears as the "iss" claim
	// in ID tokens and as the issuer in the discovery document.
	IssuerURL string

	// ClientID is the bootstrap relying-party client ID. Stella's own OIDC
	// client (OIDC_CLIENT_ID) must match this value when using the local issuer.
	ClientID string

	// ClientSecret is optional. Empty string means the client is public and
	// must use PKCE S256. Non-empty means confidential client.
	ClientSecret string

	// RedirectURIs is the exact-match allowlist of permitted redirect URIs.
	// Must contain at least one entry.
	RedirectURIs []string

	// SigningKey is the ECDSA P-256 private key used to sign ID tokens.
	// Loaded from LOCAL_OIDC_SIGNING_KEY (PEM-encoded or base64 of PEM).
	SigningKey *ecdsa.PrivateKey

	// KeyID is the "kid" value for the signing key in the JWKS response.
	// Defaults to "local-1".
	KeyID string

	// AccessTokenTTL is the access token lifetime in seconds. Default: 3600.
	AccessTokenTTL int

	// AuthCodeTTL is the authorization code lifetime in seconds. Default: 120.
	AuthCodeTTL int
}

// ConfigFromEnv reads local OIDC issuer configuration from environment variables.
//
// Required:
//
//	LOCAL_OIDC_ENABLED=true             (must be exactly "true" to enable)
//	LOCAL_OIDC_ISSUER_URL               (e.g. "http://localhost:25678/oidc/local")
//	LOCAL_OIDC_CLIENT_ID                (bootstrap client ID)
//	LOCAL_OIDC_REDIRECT_URIS            (comma-separated exact-match allowlist)
//	LOCAL_OIDC_SIGNING_KEY              (PEM-encoded ECDSA P-256 private key, or base64 of PEM)
//
// Optional:
//
//	LOCAL_OIDC_CLIENT_SECRET            (empty = public client; must use PKCE)
//	LOCAL_OIDC_KEY_ID                   (default: "local-1")
//
// Returns nil, nil when LOCAL_OIDC_ENABLED is not "true".
func ConfigFromEnv() (*Config, error) {
	if os.Getenv("LOCAL_OIDC_ENABLED") != "true" {
		return nil, nil
	}

	rawKey := os.Getenv("LOCAL_OIDC_SIGNING_KEY")
	if rawKey == "" {
		return nil, errors.New("local oidc: LOCAL_OIDC_SIGNING_KEY is required when LOCAL_OIDC_ENABLED=true")
	}
	key, err := parseSigningKey(rawKey)
	if err != nil {
		return nil, fmt.Errorf("local oidc: parse signing key: %w", err)
	}

	keyID := os.Getenv("LOCAL_OIDC_KEY_ID")
	if keyID == "" {
		keyID = "local-1"
	}

	cfg := &Config{
		IssuerURL:      os.Getenv("LOCAL_OIDC_ISSUER_URL"),
		ClientID:       os.Getenv("LOCAL_OIDC_CLIENT_ID"),
		ClientSecret:   os.Getenv("LOCAL_OIDC_CLIENT_SECRET"),
		SigningKey:     key,
		KeyID:          keyID,
		AccessTokenTTL: 3600,
		AuthCodeTTL:    120,
	}

	if raw := os.Getenv("LOCAL_OIDC_REDIRECT_URIS"); raw != "" {
		cfg.RedirectURIs = splitTrimmed(raw)
	}

	return cfg, cfg.Validate()
}

// Validate returns an error if any required field is missing or invalid.
func (c *Config) Validate() error {
	var errs []string
	if c.IssuerURL == "" {
		errs = append(errs, "LOCAL_OIDC_ISSUER_URL is required")
	}
	if c.ClientID == "" {
		errs = append(errs, "LOCAL_OIDC_CLIENT_ID is required")
	}
	if len(c.RedirectURIs) == 0 {
		errs = append(errs, "LOCAL_OIDC_REDIRECT_URIS is required")
	}
	if c.SigningKey == nil {
		errs = append(errs, "signing key is required")
	}
	if len(errs) > 0 {
		return errors.New("local oidc config: " + strings.Join(errs, "; "))
	}
	return nil
}

// IsRedirectURIAllowed reports whether uri is in the exact-match allowlist.
func (c *Config) IsRedirectURIAllowed(uri string) bool {
	return slices.Contains(c.RedirectURIs, uri)
}

// IsPublicClient reports whether the client is public (no secret).
func (c *Config) IsPublicClient() bool { return c.ClientSecret == "" }

// parseSigningKey decodes an ECDSA P-256 private key from a PEM string or
// base64-encoded PEM string.
func parseSigningKey(raw string) (*ecdsa.PrivateKey, error) {
	pemBytes := []byte(raw)

	// Try base64 decode first (for env var transport).
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw)); err == nil {
		pemBytes = decoded
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8.
		parsed, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse EC key (SEC1: %w; PKCS8: %w)", err, err2)
		}
		ecKey, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("signing key is not ECDSA")
		}
		return ecKey, nil
	}
	return key, nil
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
