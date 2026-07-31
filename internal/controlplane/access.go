package controlplane

import (
	"context"

	"github.com/CherryHQ/stella/internal/authz"
)

// Access is one authorized configuration use case. Begin mints admin-only
// access for deployment resources; BeginChannels also admits authenticated users
// and leaves personal-webhook ownership decisions to the channel methods.
type Access struct {
	svc       *Service
	authority authz.Authority
}

// Begin authorizes one control-plane use case. The control plane is administered,
// not user-owned: it validates the Authority and requires IsAdmin exactly once,
// so a non-admin or invalid authority fails closed here, before any durable read
// or external action — identical to the legacy requireAdmin gate this replaces.
// The handler never inspects identity beyond passing the trusted Authority.
func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	if !authority.Valid() || !authority.IsAdmin() {
		return nil, authz.ErrForbidden
	}
	return &Access{svc: s, authority: authority}, nil
}

// BeginChannels authorizes the resource-sensitive channels collection. Unlike
// deployment channels, webhook channels are personal resources, so every valid
// authenticated authority receives an access handle; each channel operation
// enforces its own webhook-owner/admin decision.
func (s *Service) BeginChannels(_ context.Context, authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	if !authority.Valid() {
		return nil, authz.ErrUnauthenticated
	}
	return &Access{svc: s, authority: authority}, nil
}
