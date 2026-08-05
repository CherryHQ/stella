package credential

import (
	"context"
	"crypto/subtle"
	"fmt"
)

// resolvePAT resolves a personal token. Its prefix and stored token use must
// agree so a token can never cross into another authority model.
func (s *Service) resolvePAT(ctx context.Context, raw string) (*Principal, error) {
	return s.resolveToken(ctx, raw, PATPrefix, KindPAT, TokenUsePersonal, false)
}

// resolveProvisioning resolves a restricted provisioning token. Its admin-owner
// check is intentionally here, on every bearer request, not cached at issuance.
func (s *Service) resolveProvisioning(ctx context.Context, raw string) (*Principal, error) {
	return s.resolveToken(ctx, raw, ProvisioningPrefix, KindProvisioning, TokenUseProvisioning, true)
}

// resolveToken owns the shared parse, lookup, verification, lifecycle, and
// current-owner checks for personal_access_token-backed credential kinds.
func (s *Service) resolveToken(ctx context.Context, raw, prefix string, kind Kind, expectedUse TokenUse, requireAdmin bool) (*Principal, error) {
	if s.pats == nil || s.users == nil {
		return nil, fmt.Errorf("credential: token auth not configured")
	}
	publicID, secret, err := parseOpaqueToken(prefix, raw)
	if err != nil {
		return nil, err
	}
	rec, err := s.pats.GetPATByPublicID(ctx, publicID)
	if err != nil {
		return nil, fmt.Errorf("credential: token lookup: %w", err)
	}
	if rec.TokenUse != expectedUse || !rec.TokenUse.Valid() {
		return nil, fmt.Errorf("credential: token prefix and use mismatch")
	}
	// Constant-time compare of the SHA-256 hex; the row is already narrowed to
	// one public_id, so this guards against timing oracles on the secret.
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(rec.TokenHash)) != 1 {
		return nil, fmt.Errorf("credential: token secret mismatch")
	}
	now := s.now()
	if rec.RevokedAt != nil {
		return nil, fmt.Errorf("credential: token revoked")
	}
	if rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt) {
		return nil, fmt.Errorf("credential: token expired")
	}
	ident, err := s.users.LookupUser(ctx, rec.UserID)
	if err != nil {
		return nil, fmt.Errorf("credential: token user lookup: %w", err)
	}
	if !ident.IsActive {
		return nil, fmt.Errorf("credential: token user deactivated")
	}
	if requireAdmin && !ident.IsAdmin {
		return nil, fmt.Errorf("credential: provisioning token owner is not an active admin")
	}
	if _, err := s.pats.TouchPATLastUsed(ctx, rec.ID); err != nil {
		// Best effort: a failed last_used update must not fail the request.
		s.log.Warn("credential: token last_used update failed", "error", err, "token_id", rec.ID)
	}
	credentialID := ""
	if kind == KindProvisioning {
		credentialID = rec.ID
	}
	return &Principal{
		Kind:         kind,
		UserID:       ident.UserID,
		CredentialID: credentialID,
		Username:     ident.Username,
		Email:        ident.Email,
		Name:         ident.Name,
		AvatarURL:    ident.AvatarURL,
		Role:         ident.Role,
		// Personal tokens inherit current account authority. Provisioning tokens
		// need this snapshot only for request attribution; Enforce supplies their
		// strict route allowlist.
		IsAdmin: ident.IsAdmin,
	}, nil
}
