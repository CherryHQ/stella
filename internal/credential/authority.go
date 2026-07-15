package credential

import "github.com/CherryHQ/stella/internal/authz"

// Migration adapter: credential.Principal -> authz.Authority.
//
// This adapter lives in the credential package (the trusted producer of a
// resolved bearer identity) rather than in authz, so the pure authz core does
// not import the credential resolver and the dependency direction points at
// authz. It is single-direction: there is no authz.Authority -> Principal
// reverse path.
//
// A resolved bearer credential is always a user acting through the HTTP API, so
// it maps to a UserActor, admin flag mapped straight through. The credential's
// API-permission scopes are deliberately NOT part of the Authority: the
// route-to-scope entry gate is enforced solely at credential.Enforce, so it stays
// an entry concern and never becomes an authorization capability the domains
// reason about.
func (p Principal) Authority() (authz.Authority, error) {
	return authz.NewUserAuthority(authz.UserID(p.UserID), p.IsAdmin)
}
