package oauth

import (
	"context"
	"fmt"
)

// TokenManager provides host-side token validation and reads from/writes to
// the vault.
type TokenManager struct {
	vs       VaultStore
	registry *ProviderRegistry
}

// NewTokenManager constructs a TokenManager backed by vs.
func NewTokenManager(vs VaultStore) *TokenManager {
	return &TokenManager{vs: vs}
}

// SetRegistry wires the provider registry used by GetOAuthToken.
func (m *TokenManager) SetRegistry(r *ProviderRegistry) {
	m.registry = r
}

// GetOAuthToken returns the generic OAuthBundle for providerID and userID.
// It delegates to the ProviderRegistry to load the bundle from vault.
func (m *TokenManager) GetOAuthToken(ctx context.Context, providerID string, userID string) (*OAuthBundle, error) {
	if m.registry == nil {
		return nil, fmt.Errorf("oauth: provider registry not set")
	}
	return m.registry.GetToken(ctx, m.vs, providerID, userID)
}
