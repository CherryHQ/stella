package oauth

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const tokenRefreshWindow = 10 * time.Minute

// ProviderFlowConfig holds the static configuration for one OAuth flow type
// (authorization_code or device_code) read from manifest YAML.
type ProviderFlowConfig struct {
	Type          string
	AuthURL       string
	DeviceAuthURL string
	TokenURL      string
	AuthStyle     oauth2.AuthStyle
	PKCE          bool
}

// ProviderConfig holds the static configuration for an OAuth provider read
// from manifest YAML. ClientID and ClientSecret are YAML defaults; DB overrides
// take precedence at flow-start time.
type ProviderConfig struct {
	ID           string
	Icon         string
	Scopes       []string
	VaultKey     string
	Flows        []ProviderFlowConfig
	ClientID     string
	ClientSecret string
}

// ProviderRegistry maps OAuth provider IDs to their static ProviderConfig.
// It is populated from manifest oauth_providers at runtime.
type ProviderRegistry struct {
	mu      sync.RWMutex
	entries map[string]ProviderConfig
}

// NewProviderRegistry returns an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{entries: make(map[string]ProviderConfig)}
}

// Register adds a provider's static configuration to the registry.
func (r *ProviderRegistry) Register(cfg ProviderConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[cfg.ID] = cfg
}

// Get returns the ProviderConfig for providerID, or false if not registered.
func (r *ProviderRegistry) Get(providerID string) (ProviderConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.entries[providerID]
	return cfg, ok
}

// VaultKey returns the vault key for providerID, or false if not registered.
func (r *ProviderRegistry) VaultKey(providerID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.entries[providerID]
	if !ok {
		return "", false
	}
	return cfg.VaultKey, true
}

// IDs returns all registered provider IDs in sorted order.
func (r *ProviderRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// GetToken loads the OAuthBundle for userID from vault. If the access token
// expires within tokenRefreshWindow and a refresh token is available, it
// proactively refreshes and persists the new bundle before returning. On
// refresh failure the existing bundle is returned so callers can decide what
// to do with a soon-to-expire or already-expired token.
func (r *ProviderRegistry) GetToken(ctx context.Context, vs VaultStore, providerID string, userID string) (*OAuthBundle, error) {
	cfg, ok := r.providerConfig(providerID)
	if !ok {
		return nil, fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	bundle, err := LoadOAuthBundle(ctx, vs, userID, cfg.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("oauth: get token for provider %s: %w", providerID, err)
	}
	return r.resolveToken(ctx, vs, cfg, providerID, userID, bundle)
}

func (r *ProviderRegistry) providerConfig(providerID string) (ProviderConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.entries[providerID]
	return cfg, ok
}

// resolveToken validates a loaded bundle and proactively refreshes it when the
// access token is near expiry, persisting the refreshed bundle through vs.
func (r *ProviderRegistry) resolveToken(ctx context.Context, vs VaultStore, cfg ProviderConfig, providerID string, userID string, bundle *OAuthBundle) (*OAuthBundle, error) {
	if bundle == nil {
		return nil, fmt.Errorf("oauth: user %s has not connected %s", userID, providerID)
	}
	if bundle.AccessToken == "" {
		return nil, fmt.Errorf("oauth: empty access token in vault for user %s provider %s", userID, providerID)
	}

	if needsRefresh(bundle) {
		refreshed, err := r.tryRefresh(ctx, vs, cfg, userID, bundle)
		if err != nil {
			slog.Warn("oauth: token refresh failed, using existing token",
				"provider", providerID, "user_id", userID, "error", err)
		} else {
			return refreshed, nil
		}
	}

	return bundle, nil
}

// needsRefresh returns true when the access token expires within
// tokenRefreshWindow and the bundle has a usable refresh token.
func needsRefresh(bundle *OAuthBundle) bool {
	if bundle.RefreshToken == "" {
		return false
	}
	if bundle.AccessExpiresAt.IsZero() {
		return false
	}
	if !bundle.RefreshExpiresAt.IsZero() && bundle.RefreshExpiresAt.Before(time.Now()) {
		return false // refresh token itself has expired
	}
	return time.Until(bundle.AccessExpiresAt) < tokenRefreshWindow
}

// tryRefresh exchanges the bundle's refresh token for a new access token,
// saves the updated bundle to the vault, and returns the updated bundle.
//
// Note: concurrent session starts for the same user may both attempt a refresh
// with the same refresh token. Providers that rotate refresh tokens on use
// (e.g. Feishu) will reject the second call; the caller falls back to the
// stale bundle in that case. singleflight would collapse concurrent calls if
// this becomes a problem in practice.
func (r *ProviderRegistry) tryRefresh(ctx context.Context, vs VaultStore, cfg ProviderConfig, userID string, bundle *OAuthBundle) (*OAuthBundle, error) {
	var tokenURL string
	var authStyle oauth2.AuthStyle
	for _, f := range cfg.Flows {
		if f.TokenURL != "" {
			tokenURL = f.TokenURL
			authStyle = f.AuthStyle
			break
		}
	}
	if tokenURL == "" {
		return nil, fmt.Errorf("no token URL configured for provider %s", cfg.ID)
	}

	oc := &oauth2.Config{
		ClientID:     bundle.ClientID,
		ClientSecret: bundle.ClientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: tokenURL, AuthStyle: authStyle},
	}
	// Set Expiry in the past so oauth2 unconditionally calls the token endpoint.
	stale := &oauth2.Token{
		AccessToken:  bundle.AccessToken,
		RefreshToken: bundle.RefreshToken,
		Expiry:       time.Now().Add(-time.Second),
	}
	newTok, err := oc.TokenSource(ctx, stale).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token exchange: %w", err)
	}

	updated := *bundle
	updated.AccessToken = newTok.AccessToken
	updated.AccessExpiresAt = newTok.Expiry
	if newTok.RefreshToken != "" {
		updated.RefreshToken = newTok.RefreshToken
	}
	if ri, ok := newTok.Extra("refresh_token_expires_in").(float64); ok && ri > 0 {
		updated.RefreshExpiresAt = time.Now().Add(time.Duration(ri) * time.Second)
	}

	if err := SaveOAuthBundle(ctx, vs, userID, cfg.VaultKey, updated); err != nil {
		return nil, fmt.Errorf("save refreshed token: %w", err)
	}
	slog.Info("oauth: token refreshed", "provider", cfg.ID, "user_id", userID,
		"expires_at", updated.AccessExpiresAt)
	return &updated, nil
}
