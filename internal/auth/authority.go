package auth

import "github.com/CherryHQ/stella/internal/authz"

// Migration adapter: auth.Subject -> authz.Authority.
//
// This adapter lives in the auth package (the trusted producer of a
// session-authenticated subject) rather than in authz, so the pure authz core
// does not import auth. It is single-direction: there is no reverse
// authz.Authority -> Subject path.
//
// A Subject is a cookie/OIDC-session user, so it maps to a UserActor with the
// authz role catalog. The Subject's AgentIDs are assigned-agent policy
// attributes, not identity — they are resolved at the enforcement point in
// subphase B, so this identity-only adapter deliberately drops them rather than
// widening the Authority.
func (s Subject) Authority() (authz.Authority, error) {
	roles := make([]authz.Role, 0, len(s.Roles))
	for _, r := range s.Roles {
		mapped, err := roleToAuthz(r)
		if err != nil {
			return authz.Authority{}, err
		}
		roles = append(roles, mapped)
	}
	if len(roles) == 0 {
		roles = append(roles, authz.RoleUser)
	}
	roleSet, err := authz.NewRoleSet(roles...)
	if err != nil {
		return authz.Authority{}, err
	}
	return authz.NewUserAuthority(authz.UserID(s.UserID), roleSet, authz.GrantSet{})
}

// roleToAuthz maps a legacy role string to the authz role catalog, failing
// closed on an unknown role rather than silently dropping it.
func roleToAuthz(role string) (authz.Role, error) {
	switch role {
	case RoleAdmin:
		return authz.RoleAdmin, nil
	case RoleUser:
		return authz.RoleUser, nil
	default:
		return authz.RoleInvalid, authz.ErrInvalidRole
	}
}
