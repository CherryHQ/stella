package oauth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	defaultMinValidity       = 10 * time.Minute
	concurrentReloadAttempts = 5
	concurrentReloadBaseWait = 25 * time.Millisecond
)

// TokenManager owns the load-refresh-store transaction for every OAuth client
// using one BundleStore. Save, Delete, and refresh share the same per-bundle
// lock, so a late refresh cannot resurrect a disconnected credential.
type TokenManager struct {
	vs       VaultStore // compatibility handle for user-only connection callers
	store    BundleStore
	registry *ProviderRegistry
	locks    sync.Map
}

// NewTokenManager constructs a manager over the legacy user-only vault store.
func NewTokenManager(vs VaultStore) *TokenManager {
	m := NewTokenManagerForStore(NewUserBundleStore(vs))
	m.vs = vs
	return m
}

// NewTokenManagerForStore constructs a manager over any OAuth bundle store.
func NewTokenManagerForStore(store BundleStore) *TokenManager {
	return &TokenManager{store: store}
}

// SetRegistry wires the static provider registry used by GetOAuthToken.
func (m *TokenManager) SetRegistry(r *ProviderRegistry) { m.registry = r }

// GetOAuthToken is the compatibility entry point for static connection
// providers. Dynamic clients should call GetToken with an explicit config/ref.
func (m *TokenManager) GetOAuthToken(ctx context.Context, providerID, userID string, minValidity time.Duration) (*OAuthBundle, error) {
	if m.registry == nil {
		return nil, fmt.Errorf("oauth: provider registry not set")
	}
	cfg, ok := m.registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	return m.GetToken(ctx, dynamicConfig(cfg), userBundleRef(cfg, providerID, userID), minValidity)
}

// GetToken returns a bundle whose access token clears minValidity. It reloads
// before persisting a refresh so another replica's newer authorization wins.
func (m *TokenManager) GetToken(ctx context.Context, cfg DynamicProviderConfig, ref BundleRef, minValidity time.Duration) (*OAuthBundle, error) {
	if m.store == nil {
		return nil, fmt.Errorf("oauth: bundle store not configured")
	}
	if minValidity <= 0 {
		minValidity = defaultMinValidity
	}
	return withManagerLock(m, ref, func() (*OAuthBundle, error) {
		bundle, err := m.store.Load(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("oauth: get token for provider %s: %w", ref.ProviderKey, err)
		}
		return m.resolve(ctx, cfg, ref, bundle, minValidity)
	})
}

// Save serializes authorization completion with refresh and disconnect.
func (m *TokenManager) Save(ctx context.Context, ref BundleRef, bundle OAuthBundle) error {
	if m.store == nil {
		return fmt.Errorf("oauth: bundle store not configured")
	}
	_, err := withManagerLock(m, ref, func() (*OAuthBundle, error) {
		return nil, m.store.Save(ctx, ref, bundle)
	})
	return err
}

// Load reads a bundle without refreshing it. Status projections use this to
// avoid network work while preserving the shared storage format.
func (m *TokenManager) Load(ctx context.Context, ref BundleRef) (*OAuthBundle, error) {
	if m.store == nil {
		return nil, fmt.Errorf("oauth: bundle store not configured")
	}
	return m.store.Load(ctx, ref)
}

// Delete serializes disconnect with refresh and authorization completion.
func (m *TokenManager) Delete(ctx context.Context, ref BundleRef) error {
	if m.store == nil {
		return fmt.Errorf("oauth: bundle store not configured")
	}
	_, err := withManagerLock(m, ref, func() (*OAuthBundle, error) {
		return nil, m.store.Delete(ctx, ref)
	})
	return err
}

// TokenSource returns a stateless source that reloads the persisted bundle on
// every call, so reconnects performed by another process become visible.
func (m *TokenManager) TokenSource(ctx context.Context, cfg DynamicProviderConfig, ref BundleRef, minValidity time.Duration) oauth2.TokenSource {
	return tokenSourceFunc(func() (*oauth2.Token, error) {
		bundle, err := m.GetToken(ctx, cfg, ref, minValidity)
		if err != nil {
			return nil, err
		}
		return &oauth2.Token{
			AccessToken: bundle.AccessToken, TokenType: "Bearer",
			RefreshToken: bundle.RefreshToken, Expiry: bundle.AccessExpiresAt,
		}, nil
	})
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

func withManagerLock[T any](m *TokenManager, ref BundleRef, fn func() (T, error)) (T, error) {
	owner := ref.Owner
	key := strings.Join([]string{ref.ProviderKey, owner.Scope, owner.UserID, owner.AgentID, ref.Name}, "\x00")
	entry, _ := m.locks.LoadOrStore(key, &sync.Mutex{})
	mu := entry.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

func (m *TokenManager) resolve(ctx context.Context, cfg DynamicProviderConfig, ref BundleRef, bundle *OAuthBundle, minValidity time.Duration) (*OAuthBundle, error) {
	if bundle == nil {
		return nil, fmt.Errorf("oauth: user %s has not connected %s", ref.Owner.UserID, ref.ProviderKey)
	}
	if bundle.AccessToken == "" {
		return nil, fmt.Errorf("oauth: empty access token in vault for user %s provider %s", ref.Owner.UserID, ref.ProviderKey)
	}
	if needsRefresh(bundle, minValidity) {
		refreshed, err := m.tryRefresh(ctx, cfg, ref, bundle)
		if err == nil && meetsMinValidity(refreshed, minValidity) {
			return refreshed, nil
		}
		if err == nil {
			err = fmt.Errorf("provider returned a token below the required %s validity", minValidity)
		}
		slog.Warn("oauth: token refresh failed, checking vault for a concurrently refreshed bundle",
			"provider", ref.ProviderKey, "scope", ref.Owner.Scope, "user_id", ref.Owner.UserID, "agent_id", ref.Owner.AgentID, "error", err)
		if reloaded := m.reloadUsable(ctx, ref, minValidity); reloaded != nil {
			return reloaded, nil
		}
	}
	if meetsMinValidity(bundle, minValidity) {
		return bundle, nil
	}
	return nil, fmt.Errorf("oauth: provider %s token is expired or below the required %s validity; reconnect the provider", ref.ProviderKey, minValidity)
}

func needsRefresh(bundle *OAuthBundle, minValidity time.Duration) bool {
	if bundle.RefreshToken == "" || bundle.AccessExpiresAt.IsZero() {
		return false
	}
	if !bundle.RefreshExpiresAt.IsZero() && bundle.RefreshExpiresAt.Before(time.Now()) {
		return false
	}
	return time.Until(bundle.AccessExpiresAt) < minValidity
}

func meetsMinValidity(bundle *OAuthBundle, minValidity time.Duration) bool {
	return bundle != nil && (bundle.AccessExpiresAt.IsZero() || time.Until(bundle.AccessExpiresAt) >= minValidity)
}

func (m *TokenManager) reloadUsable(ctx context.Context, ref BundleRef, minValidity time.Duration) *OAuthBundle {
	for attempt := range concurrentReloadAttempts {
		reloaded, err := m.store.Load(ctx, ref)
		if err == nil && reloaded != nil && reloaded.AccessToken != "" && meetsMinValidity(reloaded, minValidity) {
			return reloaded
		}
		if attempt == concurrentReloadAttempts-1 {
			break
		}
		timer := time.NewTimer(concurrentReloadBaseWait << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (m *TokenManager) tryRefresh(ctx context.Context, cfg DynamicProviderConfig, ref BundleRef, bundle *OAuthBundle) (*OAuthBundle, error) {
	tokenURL := bundle.TokenEndpoint
	if tokenURL == "" {
		tokenURL = cfg.TokenURL
	}
	if tokenURL == "" {
		return nil, fmt.Errorf("no token URL configured for provider %s", ref.ProviderKey)
	}
	clientID := bundle.ClientID
	if clientID == "" {
		clientID = cfg.ClientID
	}
	clientSecret := bundle.ClientSecret
	if clientSecret == "" {
		clientSecret = cfg.ClientSecret
	}
	authStyle := oauth2.AuthStyle(bundle.AuthStyle)
	if bundle.AuthStyle == 0 {
		authStyle = cfg.AuthStyle
	}
	resource := bundle.Resource
	if resource == "" {
		resource = cfg.Resource
	}
	refreshCtx := ctx
	if cfg.HTTPClient != nil {
		client := cfg.HTTPClient
		if resource != "" {
			clone := *client
			clone.Transport = resourceRoundTripper{base: client.Transport, tokenURL: tokenURL, resource: resource}
			client = &clone
		}
		refreshCtx = context.WithValue(ctx, oauth2.HTTPClient, client)
	}
	oc := &oauth2.Config{
		ClientID: clientID, ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{TokenURL: tokenURL, AuthStyle: authStyle},
	}
	stale := &oauth2.Token{AccessToken: bundle.AccessToken, RefreshToken: bundle.RefreshToken, Expiry: time.Now().Add(-time.Second)}
	newTok, err := oc.TokenSource(refreshCtx, stale).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token exchange: %w", err)
	}

	updated := *bundle
	updated.ClientID = clientID
	updated.TokenEndpoint = tokenURL
	updated.AuthStyle = int(authStyle)
	updated.Resource = resource
	updated.AccessToken = newTok.AccessToken
	updated.AccessExpiresAt = newTok.Expiry
	if newTok.RefreshToken != "" {
		updated.RefreshToken = newTok.RefreshToken
	}
	if ri, ok := newTok.Extra("refresh_token_expires_in").(float64); ok && ri > 0 {
		updated.RefreshExpiresAt = time.Now().Add(time.Duration(ri) * time.Second)
	}
	if scope, ok := newTok.Extra("scope").(string); ok && scope != "" {
		updated.GrantedScope = scope
	}

	latest, err := m.store.Load(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("reload bundle before refresh save: %w", err)
	}
	if bundleChangedSinceRefreshStarted(bundle, latest) {
		if latest == nil {
			return nil, fmt.Errorf("oauth bundle was removed during refresh")
		}
		return latest, nil
	}
	if err := m.store.Save(ctx, ref, updated); err != nil {
		return nil, fmt.Errorf("save refreshed token: %w", err)
	}
	slog.Info("oauth: token refreshed", "provider", ref.ProviderKey, "scope", ref.Owner.Scope, "user_id", ref.Owner.UserID, "agent_id", ref.Owner.AgentID, "expires_at", updated.AccessExpiresAt)
	return &updated, nil
}

func bundleChangedSinceRefreshStarted(original, latest *OAuthBundle) bool {
	if latest == nil {
		return original != nil
	}
	if original == nil {
		return true
	}
	return latest.ClientID != original.ClientID || latest.AccessToken != original.AccessToken ||
		latest.RefreshToken != original.RefreshToken || latest.GrantedScope != original.GrantedScope ||
		!slices.Equal(latest.DesiredScopes, original.DesiredScopes)
}

// resourceRoundTripper adds RFC 8707's resource parameter to refresh grants.
// oauth2.Config exposes exchange options but not refresh options, so the shared
// manager injects the parameter at the one token endpoint request boundary.
type resourceRoundTripper struct {
	base     http.RoundTripper
	tokenURL string
	resource string
}

func (t resourceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.Method != http.MethodPost || req.URL.String() != t.tokenURL || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		return base.RoundTrip(req)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("oauth: read token request: %w", err)
	}
	req.Body = io.NopCloser(strings.NewReader(string(raw)))
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, fmt.Errorf("oauth: parse token request: %w", err)
	}
	form.Set("resource", t.resource)
	body := form.Encode()
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(strings.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }
	return base.RoundTrip(clone)
}

func dynamicConfig(cfg ProviderConfig) DynamicProviderConfig {
	var tokenURL string
	var authStyle oauth2.AuthStyle
	for _, flow := range cfg.Flows {
		if flow.TokenURL != "" {
			tokenURL = flow.TokenURL
			authStyle = flow.AuthStyle
			break
		}
	}
	return DynamicProviderConfig{
		ProviderKey: cfg.ID, TokenURL: tokenURL, AuthStyle: authStyle,
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
	}
}

func userBundleRef(cfg ProviderConfig, providerID, userID string) BundleRef {
	return BundleRef{ProviderKey: providerID, Owner: CredentialOwner{Scope: "user", UserID: userID}, Name: cfg.VaultKey}
}
