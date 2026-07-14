package controlplane

import (
	"context"

	"github.com/CherryHQ/stella/internal/authz"
)

// Access is one authorized control-plane use case. Every control-plane resource
// (providers, settings, plugins, channels) is admin-only, so authorization is a
// single gate at Begin: an Access exists only for an admin authority. Each method
// then performs the durable write and hot-reload the legacy handler did, with no
// further per-method authorization.
type Access struct {
	svc *Service
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
	return &Access{svc: s}, nil
}
