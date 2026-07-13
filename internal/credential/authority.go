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
// it maps to a UserActor. The credential's API-permission scopes become typed
// entry-scope grants — the route-to-scope entry gate is a per-user private
// capability, so these grants are holdable by a UserActor and would be rejected
// on a group actor. admin/user roles map to the authz role catalog.
func (p Principal) Authority() (authz.Authority, error) {
	roles := []authz.Role{authz.RoleUser}
	if p.IsAdmin {
		roles = append(roles, authz.RoleAdmin)
	}
	roleSet, err := authz.NewRoleSet(roles...)
	if err != nil {
		return authz.Authority{}, err
	}

	grants := make([]authz.Grant, 0, len(p.Scopes))
	for _, scope := range p.Scopes {
		g, err := authz.EntryScopeGrant(scope)
		if err != nil {
			return authz.Authority{}, err
		}
		grants = append(grants, g)
	}
	grantSet, err := authz.NewGrantSet(grants...)
	if err != nil {
		return authz.Authority{}, err
	}

	return authz.NewUserAuthority(authz.UserID(p.UserID), roleSet, grantSet)
}
