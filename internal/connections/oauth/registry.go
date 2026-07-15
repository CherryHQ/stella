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

// defaultMinValidity is the remaining-lifetime floor a caller gets when it does
// not request one explicitly: refresh (or reject) a token with less than this
// left. Session-scoped callers pass a larger value derived from the chat timeout
// (#722); short-lived callers (e.g. a one-shot GitHub API call) rely on this.
const (
	defaultMinValidity       = 10 * time.Minute
	concurrentReloadAttempts = 5
	concurrentReloadBaseWait = 25 * time.Millisecond
)

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

// VaultKeys returns all non-empty provider vault keys in sorted order.
func (r *ProviderRegistry) VaultKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.entries))
	for _, cfg := range r.entries {
		if cfg.VaultKey == "" {
			continue
		}
		keys = append(keys, cfg.VaultKey)
	}
	sort.Strings(keys)
	return keys
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

// GetToken loads the OAuthBundle for userID from vault and returns a token that
// remains valid for at least minValidity (0 uses defaultMinValidity). If the
// access token would fall below that floor and a refresh token is available it
// proactively refreshes and persists the new bundle. On refresh failure it
// briefly reloads the vault to consume a bundle another replica may have just
// persisted, and never returns an expired or below-floor token — see
// resolveToken.
func (r *ProviderRegistry) GetToken(ctx context.Context, vs VaultStore, providerID string, userID string, minValidity time.Duration) (*OAuthBundle, error) {
	cfg, ok := r.providerConfig(providerID)
	if !ok {
		return nil, fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	bundle, err := LoadOAuthBundle(ctx, vs, userID, cfg.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("oauth: get token for provider %s: %w", providerID, err)
	}
	return r.resolveToken(ctx, vs, cfg, providerID, userID, bundle, minValidity)
}

func (r *ProviderRegistry) providerConfig(providerID string) (ProviderConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.entries[providerID]
	return cfg, ok
}

// resolveToken returns a token good for at least minValidity (0 uses
// defaultMinValidity). It refreshes a bundle whose access token would fall below
// that floor and persists the result through vs. If the refresh fails it reloads
// the vault once to consume a bundle a concurrent replica may have just written,
// rather than coordinating through a lock. It never returns an expired or
// below-floor token: a still-valid bundle that clears the floor is returned,
// otherwise an actionable error tells the caller to reconnect.
func (r *ProviderRegistry) resolveToken(ctx context.Context, vs VaultStore, cfg ProviderConfig, providerID string, userID string, bundle *OAuthBundle, minValidity time.Duration) (*OAuthBundle, error) {
	if minValidity <= 0 {
		minValidity = defaultMinValidity
	}
	if bundle == nil {
		return nil, fmt.Errorf("oauth: user %s has not connected %s", userID, providerID)
	}
	if bundle.AccessToken == "" {
		return nil, fmt.Errorf("oauth: empty access token in vault for user %s provider %s", userID, providerID)
	}

	if needsRefresh(bundle, minValidity) {
		refreshed, err := r.tryRefresh(ctx, vs, cfg, userID, bundle)
		if err == nil {
			if meetsMinValidity(refreshed, minValidity) {
				return refreshed, nil
			}
			err = fmt.Errorf("provider returned a token below the required %s validity", minValidity)
		}
		// A concurrent replica sharing this user's vault may have rotated the
		// refresh token and be about to persist a fresh bundle, which makes our
		// reused refresh token fail. Briefly poll the vault for that winner's
		// bundle instead of introducing a distributed lock.
		slog.Warn("oauth: token refresh failed, checking vault for a concurrently refreshed bundle",
			"provider", providerID, "user_id", userID, "error", err)
		if reloaded := r.reloadUsable(ctx, vs, cfg, userID, minValidity); reloaded != nil {
			return reloaded, nil
		}
	}

	// Never hand back a token that can't cover minValidity — that includes an
	// already-expired one. A still-valid token that clears the floor (e.g. one
	// with no known expiry, or refreshed by another path) is fine to serve.
	if meetsMinValidity(bundle, minValidity) {
		return bundle, nil
	}
	return nil, fmt.Errorf("oauth: provider %s token for user %s is expired or below the required %s validity and could not be refreshed; reconnect the provider", providerID, userID, minValidity)
}

// needsRefresh returns true when the access token would drop below minValidity
// and the bundle has a usable refresh token to trade for a new one.
func needsRefresh(bundle *OAuthBundle, minValidity time.Duration) bool {
	if bundle.RefreshToken == "" {
		return false
	}
	if bundle.AccessExpiresAt.IsZero() {
		return false
	}
	if !bundle.RefreshExpiresAt.IsZero() && bundle.RefreshExpiresAt.Before(time.Now()) {
		return false // refresh token itself has expired
	}
	return time.Until(bundle.AccessExpiresAt) < minValidity
}

// meetsMinValidity reports whether the bundle's access token stays valid for at
// least minValidity. A bundle with no known expiry (AccessExpiresAt zero) is
// treated as long-lived — e.g. a GitHub OAuth-app token — and always qualifies.
func meetsMinValidity(bundle *OAuthBundle, minValidity time.Duration) bool {
	if bundle.AccessExpiresAt.IsZero() {
		return true
	}
	return time.Until(bundle.AccessExpiresAt) >= minValidity
}

// reloadUsable briefly polls the vault and returns only a bundle that clears
// minValidity. A refresh loser can receive invalid_grant just before the winning
// replica commits its rotated bundle, so one immediate read is racy. The bounded
// exponential wait keeps that convergence path lock-free and honors ctx.
func (r *ProviderRegistry) reloadUsable(ctx context.Context, vs VaultStore, cfg ProviderConfig, userID string, minValidity time.Duration) *OAuthBundle {
	for attempt := range concurrentReloadAttempts {
		reloaded, err := LoadOAuthBundle(ctx, vs, userID, cfg.VaultKey)
		if err == nil && reloaded != nil && reloaded.AccessToken != "" && meetsMinValidity(reloaded, minValidity) {
			return reloaded
		}
		if attempt == concurrentReloadAttempts-1 {
			break
		}

		wait := concurrentReloadBaseWait << attempt
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
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
