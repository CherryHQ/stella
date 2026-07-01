package credential

import (
	"context"
	"fmt"
)

// resolveLegacy is the STELLA_TOKEN sub-resolver: the vault-injected, 90-day
// auto token. It is dispatched only for bearers that are not a reserved family
// prefix. Verification is delegated to the token backend (auth_user_token
// lookup); this adapter maps the result into a Principal of kind
// legacy_stella_token, which Enforce lets bypass API-scope checks (handler
// ownership/admin checks still apply).
func (s *Service) resolveLegacy(ctx context.Context, raw string) (*Principal, error) {
	if s.tokens == nil {
		return nil, fmt.Errorf("credential: token auth not configured")
	}
	ident, err := s.tokens.AuthenticateLegacy(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !ident.IsActive {
		return nil, fmt.Errorf("credential: token user deactivated")
	}
	return &Principal{
		Kind:      KindLegacyStellaToken,
		UserID:    ident.UserID,
		Username:  ident.Username,
		Email:     ident.Email,
		Name:      ident.Name,
		AvatarURL: ident.AvatarURL,
		Role:      ident.Role,
		IsAdmin:   ident.IsAdmin,
	}, nil
}
