package oauth

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/oauth2"
)

// ProviderFlowConfig holds the static configuration for one OAuth flow type
// (authorization_code or device_code) read from manifest YAML.
type ProviderFlowConfig struct {
	Type          string
	AuthURL       string
	DeviceAuthURL string
	TokenURL      string
	AuthStyle     oauth2.AuthStyle
}

// ProviderConfig holds the static configuration for an OAuth provider read
// from manifest YAML. Credentials are not baked in — they are fetched from
// plugin config at flow-start time so admin UI edits take effect immediately.
type ProviderConfig struct {
	ID       string
	Scopes   []string
	VaultKey string
	Flows    []ProviderFlowConfig
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

// GetToken loads the OAuthBundle for userID from vault using the provider's
// registered vault key.
func (r *ProviderRegistry) GetToken(ctx context.Context, vs VaultStore, providerID string, userID int64) (*OAuthBundle, error) {
	r.mu.RLock()
	cfg, ok := r.entries[providerID]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	bundle, err := LoadOAuthBundle(ctx, vs, userID, cfg.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("oauth: get token for provider %s: %w", providerID, err)
	}
	if bundle == nil {
		return nil, fmt.Errorf("oauth: user %d has not connected %s", userID, providerID)
	}
	if bundle.AccessToken == "" {
		return nil, fmt.Errorf("oauth: empty access token in vault for user %d provider %s", userID, providerID)
	}
	return bundle, nil
}
