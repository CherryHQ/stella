package credential

import (
	"context"
	"crypto/subtle"
	"fmt"
)

// resolvePAT is the personal-access-token sub-resolver. It owns the full PAT
// path: parse + checksum, indexed public_id lookup, constant-time hash compare,
// expiry/revocation, active-user check, and a throttled last_used touch.
func (s *Service) resolvePAT(ctx context.Context, raw string) (*Principal, error) {
	if s.pats == nil || s.users == nil {
		return nil, fmt.Errorf("credential: PAT auth not configured")
	}
	publicID, secret, err := parseOpaqueToken(PATPrefix, raw)
	if err != nil {
		return nil, err
	}
	rec, err := s.pats.GetPATByPublicID(ctx, publicID)
	if err != nil {
		return nil, fmt.Errorf("credential: PAT lookup: %w", err)
	}
	// Constant-time compare of the SHA-256 hex; the row is already narrowed to
	// one public_id, so this guards against timing oracles on the secret.
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(rec.TokenHash)) != 1 {
		return nil, fmt.Errorf("credential: PAT secret mismatch")
	}
	now := s.now()
	if rec.RevokedAt != nil {
		return nil, fmt.Errorf("credential: PAT revoked")
	}
	if rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt) {
		return nil, fmt.Errorf("credential: PAT expired")
	}
	ident, err := s.users.LookupUser(ctx, rec.UserID)
	if err != nil {
		return nil, fmt.Errorf("credential: PAT user lookup: %w", err)
	}
	if !ident.IsActive {
		return nil, fmt.Errorf("credential: PAT user deactivated")
	}
	if _, err := s.pats.TouchPATLastUsed(ctx, rec.ID); err != nil {
		// Best effort: a failed last_used update must not fail the request.
		s.log.Warn("credential: PAT last_used update failed", "error", err, "pat_id", rec.ID)
	}
	return &Principal{
		Kind:      KindPAT,
		UserID:    ident.UserID,
		Scopes:    rec.Scopes,
		Username:  ident.Username,
		Email:     ident.Email,
		Name:      ident.Name,
		AvatarURL: ident.AvatarURL,
		Role:      ident.Role,
		// Phase 1: PATs never carry admin. Least privilege -- handler admin gates
		// (requireAdmin) fail closed for PATs, and admin routes are not exposed to
		// bearer credentials anyway.
		IsAdmin: false,
	}, nil
}
