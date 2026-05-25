// Package localoidc implements a minimal built-in OIDC issuer for Stella.
// It is designed for development and single-user/self-contained deployments.
// The issuer supports authorization-code + PKCE only.
package localoidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// SettingStore is the subset of config.Store used for key persistence.
type SettingStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Config holds the configuration for the built-in local OIDC issuer.
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
	SigningKey *ecdsa.PrivateKey

	// KeyID is the "kid" value for the signing key in the JWKS response.
	// Defaults to "local-1".
	KeyID string

	// AccessTokenTTL is the access token lifetime in seconds. Default: 3600.
	AccessTokenTTL int

	// AuthCodeTTL is the authorization code lifetime in seconds. Default: 120.
	AuthCodeTTL int
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

const (
	settingKeySigningKey = "local_oidc_signing_key"

	AutoClientID = "stella"
	AutoKeyID    = "local-1"
)

// LoadOrGenerateSigningKey loads the ECDSA P-256 signing key from the settings
// store, generating and persisting a new one on first call. This is the only
// state that needs to survive restarts for the built-in local OIDC issuer;
// all URL-based config (IssuerURL, RedirectURIs) is assembled by the caller.
func LoadOrGenerateSigningKey(ctx context.Context, store SettingStore) (*ecdsa.PrivateKey, error) {
	return loadOrGenerateSigningKey(ctx, store)
}

func loadOrGenerateSigningKey(ctx context.Context, store SettingStore) (*ecdsa.PrivateKey, error) {
	raw, err := store.GetSetting(ctx, settingKeySigningKey)
	if err == nil && raw != "" {
		return parseSigningKey(raw)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	encoded := base64.StdEncoding.EncodeToString(pemBytes)

	if saveErr := store.SetSetting(ctx, settingKeySigningKey, encoded); saveErr != nil {
		return nil, fmt.Errorf("persist signing key: %w", saveErr)
	}
	return key, nil
}
