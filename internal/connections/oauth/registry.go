package oauth

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// ProviderFlowConfig holds the static configuration for one OAuth flow type.
type ProviderFlowConfig struct {
	Type          string
	AuthURL       string
	DeviceAuthURL string
	TokenURL      string
	AuthStyle     oauth2.AuthStyle
	PKCE          bool
}

// ProviderConfig holds one manifest-defined OAuth provider. Dynamic clients
// such as MCP adapt discovery output directly to DynamicProviderConfig.
type ProviderConfig struct {
	ID           string
	Icon         string
	Scopes       []string
	VaultKey     string
	Flows        []ProviderFlowConfig
	ClientID     string
	ClientSecret string
}

// ProviderRegistry maps connection provider IDs to static configuration. Token
// lifecycle is delegated to TokenManager; the registry is lookup policy only.
type ProviderRegistry struct {
	mu      sync.RWMutex
	entries map[string]ProviderConfig

	managerMu sync.Mutex
	manager   *TokenManager
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{entries: make(map[string]ProviderConfig)}
}

func (r *ProviderRegistry) Register(cfg ProviderConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[cfg.ID] = cfg
}

func (r *ProviderRegistry) Get(providerID string) (ProviderConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.entries[providerID]
	return cfg, ok
}

func (r *ProviderRegistry) VaultKey(providerID string) (string, bool) {
	cfg, ok := r.Get(providerID)
	return cfg.VaultKey, ok
}

func (r *ProviderRegistry) VaultKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.entries))
	for _, cfg := range r.entries {
		if cfg.VaultKey != "" {
			keys = append(keys, cfg.VaultKey)
		}
	}
	sort.Strings(keys)
	return keys
}

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

// GetToken is kept for callers that use the registry as their connection
// facade; the shared manager owns all token behavior.
func (r *ProviderRegistry) GetToken(ctx context.Context, vs VaultStore, providerID, userID string, minValidity time.Duration) (*OAuthBundle, error) {
	cfg, ok := r.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	return r.tokenManager(vs).GetToken(ctx, dynamicConfig(cfg), userBundleRef(cfg, providerID, userID), minValidity)
}

func (r *ProviderRegistry) SaveBundle(ctx context.Context, vs VaultStore, providerID, userID string, bundle OAuthBundle) error {
	cfg, ok := r.Get(providerID)
	if !ok {
		return fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	return r.tokenManager(vs).Save(ctx, userBundleRef(cfg, providerID, userID), bundle)
}

func (r *ProviderRegistry) DeleteBundle(ctx context.Context, vs VaultStore, providerID, userID string) error {
	cfg, ok := r.Get(providerID)
	if !ok {
		return fmt.Errorf("oauth: unknown provider: %s", providerID)
	}
	return r.tokenManager(vs).Delete(ctx, userBundleRef(cfg, providerID, userID))
}

func (r *ProviderRegistry) tokenManager(vs VaultStore) *TokenManager {
	r.managerMu.Lock()
	defer r.managerMu.Unlock()
	if r.manager == nil {
		r.manager = NewTokenManager(vs)
		r.manager.SetRegistry(r)
	}
	return r.manager
}

// SetTokenManager makes registry compatibility calls share the service's lock
// domain with authorization completion and disconnect.
func (r *ProviderRegistry) SetTokenManager(manager *TokenManager) {
	r.managerMu.Lock()
	defer r.managerMu.Unlock()
	r.manager = manager
	if manager != nil {
		manager.SetRegistry(r)
	}
}
