package zitadel

import (
	"context"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc"
)

// defaultScopes are the scopes recommended for ZITADEL to get email, profile,
// and (if org claims are configured) org information.
var defaultScopes = []string{"openid", "email", "profile"}

// NewProvider creates an auth.AuthProvider backed by a generic OIDC provider
// configured for ZITADEL. If cfg.Scopes is empty the ZITADEL-recommended
// defaults are used.
//
// Future ZITADEL-specific extensions (e.g. org-aware claim mapping, role
// projection scopes) can be added here without touching the generic provider.
func NewProvider(ctx context.Context, cfg *oidc.Config) (auth.AuthProvider, error) {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = defaultScopes
	}
	return oidc.NewProvider(ctx, cfg)
}
