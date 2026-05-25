package localoidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/internal/auth"
)

const localProviderName = "local"

// ClientProvider implements auth.AuthProvider for the built-in local OIDC issuer.
// Unlike the generic oidc.Provider, it does not perform OIDC discovery at startup —
// it derives endpoints from Config and verifies tokens using the in-memory signing
// key. This avoids the chicken-and-egg problem of discovering the issuer before its
// HTTP server has started.
type ClientProvider struct {
	cfg       *Config
	oauth2Cfg *oauth2.Config
	verifier  *gooidc.IDTokenVerifier
}

// NewClientProvider creates a ClientProvider from cfg. redirectURI is the callback
// URL that will receive the authorization code; it must be in cfg.RedirectURIs.
// No HTTP discovery is performed.
func NewClientProvider(cfg *Config, redirectURI string) (*ClientProvider, error) {
	if cfg.SigningKey == nil {
		return nil, errors.New("local oidc client: signing key is required")
	}
	keySet := &ecPublicKeySet{pub: &cfg.SigningKey.PublicKey}
	verifier := gooidc.NewVerifier(cfg.IssuerURL, keySet, &gooidc.Config{
		ClientID: cfg.ClientID,
	})
	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  redirectURI,
		Endpoint: oauth2.Endpoint{
			AuthURL:  cfg.IssuerURL + "/authorize",
			TokenURL: cfg.IssuerURL + "/token",
		},
		Scopes: []string{"openid", "email", "profile"},
	}
	return &ClientProvider{cfg: cfg, oauth2Cfg: oauth2Cfg, verifier: verifier}, nil
}

// Name returns "local".
func (p *ClientProvider) Name() string { return localProviderName }

// LoginURL builds the PKCE authorization URL for the local issuer.
func (p *ClientProvider) LoginURL(_ context.Context, state auth.AuthState) (string, error) {
	sum := sha256.Sum256([]byte(state.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	url := p.oauth2Cfg.AuthCodeURL(state.State,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	return url, nil
}

// HandleCallback exchanges the authorization code, verifies the ID token, and
// returns a normalised ExternalIdentity.
func (p *ClientProvider) HandleCallback(ctx context.Context, r *http.Request, state auth.AuthState) (*auth.ExternalIdentity, error) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		return nil, fmt.Errorf("local oidc: IdP error %q: %s", errParam, r.URL.Query().Get("error_description"))
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, errors.New("local oidc client: missing code in callback")
	}

	token, err := p.oauth2Cfg.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", state.CodeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("local oidc client: exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("local oidc client: id_token missing from token response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("local oidc client: verify id_token: %w", err)
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("local oidc client: extract claims: %w", err)
	}

	email, _ := claims["email"].(string)
	if email == "" {
		return nil, errors.New("local oidc client: email claim missing")
	}
	emailVerified, _ := claims["email_verified"].(bool)
	if !emailVerified {
		return nil, errors.New("local oidc client: email not verified")
	}

	name, _ := claims["name"].(string)
	if name == "" {
		name, _ = claims["preferred_username"].(string)
	}
	picture, _ := claims["picture"].(string)

	return &auth.ExternalIdentity{
		Provider:  localProviderName,
		Subject:   idToken.Subject,
		Email:     email,
		Name:      name,
		AvatarURL: picture,
		Claims:    claims,
	}, nil
}

// ecPublicKeySet implements gooidc.KeySet using a known ECDSA P-256 public key.
// No HTTP requests are made.
type ecPublicKeySet struct {
	pub *ecdsa.PublicKey
}

// VerifySignature implements gooidc.KeySet: it verifies the compact JWS signature
// and returns the raw JSON payload bytes.
func (k *ecPublicKeySet) VerifySignature(_ context.Context, jwtStr string) ([]byte, error) {
	if _, err := VerifyES256(jwtStr, k.pub); err != nil {
		return nil, err
	}
	parts := strings.SplitN(jwtStr, ".", 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return payload, nil
}
