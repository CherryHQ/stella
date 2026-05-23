package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/internal/auth"
)

// Provider implements auth.AuthProvider for a generic OIDC identity provider.
// It uses OIDC Discovery to find endpoints and go-oidc to verify ID tokens.
type Provider struct {
	cfg      *Config
	verifier *gooidc.IDTokenVerifier
	oauth2   *oauth2.Config
}

// NewProvider creates a Provider, performing OIDC Discovery against cfg.IssuerURL.
// The context is used only for the discovery HTTP request.
func NewProvider(ctx context.Context, cfg *Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	oidcProvider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover provider %q: %w", cfg.IssuerURL, err)
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     oidcProvider.Endpoint(),
		Scopes:       cfg.Scopes,
	}

	verifier := oidcProvider.Verifier(&gooidc.Config{ClientID: cfg.ClientID})

	return &Provider{
		cfg:      cfg,
		verifier: verifier,
		oauth2:   oauth2Cfg,
	}, nil
}

// Name returns the stable provider name used in routes and DB records.
func (p *Provider) Name() string { return p.cfg.ProviderName }

// LoginURL generates the OAuth2 authorization URL with PKCE (S256) and CSRF state.
func (p *Provider) LoginURL(ctx context.Context, state auth.AuthState) (string, error) {
	challenge, method, err := pkceChallenge(state.CodeVerifier)
	if err != nil {
		return "", fmt.Errorf("oidc: generate PKCE challenge: %w", err)
	}

	url := p.oauth2.AuthCodeURL(
		state.State,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", method),
	)
	return url, nil
}

// HandleCallback validates the IdP callback, exchanges the auth code, verifies
// the ID token, and returns the normalised ExternalIdentity.
//
// Rejects logins where email_verified is false or email is empty.
// Extracts org claims using the configured OrgIDClaim / OrgNameClaim keys.
func (p *Provider) HandleCallback(ctx context.Context, r *http.Request, state auth.AuthState) (*auth.ExternalIdentity, error) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		return nil, fmt.Errorf("oidc: IdP returned error %q: %s", errParam, desc)
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, errors.New("oidc: missing code in callback")
	}

	// Exchange auth code for tokens with PKCE verifier.
	oauth2Token, err := p.oauth2.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", state.CodeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("oidc: exchange code: %w", err)
	}

	// Extract and verify ID token.
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("oidc: id_token missing from token response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: extract claims: %w", err)
	}

	email, _ := claims["email"].(string)
	if email == "" {
		return nil, errors.New("oidc: email claim missing")
	}

	emailVerified, _ := claims["email_verified"].(bool)
	if !emailVerified {
		return nil, errors.New("oidc: email not verified by IdP")
	}

	name, _ := claims["name"].(string)
	if name == "" {
		name, _ = claims["preferred_username"].(string)
	}
	picture, _ := claims["picture"].(string)

	orgID, orgName := p.extractOrgClaims(claims)

	// Strip sensitive fields before storing raw claims.
	filtered := filterClaims(claims)

	return &auth.ExternalIdentity{
		Provider:  p.cfg.ProviderName,
		Subject:   idToken.Subject,
		Email:     email,
		Name:      name,
		AvatarURL: picture,
		OrgID:     orgID,
		OrgName:   orgName,
		Claims:    filtered,
	}, nil
}

// extractOrgClaims reads the configured org claim keys from the token claims.
func (p *Provider) extractOrgClaims(claims map[string]any) (orgID, orgName string) {
	if p.cfg.OrgIDClaim != "" {
		orgID, _ = claims[p.cfg.OrgIDClaim].(string)
	}
	if p.cfg.OrgNameClaim != "" {
		orgName, _ = claims[p.cfg.OrgNameClaim].(string)
	}
	return orgID, orgName
}

// filterClaims returns a copy of claims without fields that should never be
// persisted (nonce, c_hash, at_hash, etc.).
func filterClaims(claims map[string]any) map[string]any {
	drop := map[string]bool{"nonce": true, "c_hash": true, "at_hash": true, "s_hash": true}
	out := make(map[string]any, len(claims))
	for k, v := range claims {
		if !drop[k] {
			out[k] = v
		}
	}
	return out
}
