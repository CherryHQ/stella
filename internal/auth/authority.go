package auth

import (
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
)

// Migration adapter: auth.Subject -> authz.Authority.
//
// This adapter lives in the auth package (the trusted producer of a
// session-authenticated subject) rather than in authz, so the pure authz core
// does not import auth. It is single-direction: there is no reverse
// authz.Authority -> Subject path.
//
// A Subject is a cookie/OIDC-session user, so it maps to a UserActor. Its role
// strings are validated against the known catalog and collapse to a single admin
// bool; an unknown role fails closed rather than being silently dropped, and no
// role (the default) means an ordinary user. The Subject's AgentIDs are
// assigned-agent policy attributes, not identity — they are resolved at the
// enforcement point, so this identity-only adapter deliberately drops them.
func (s Subject) Authority() (authz.Authority, error) {
	admin, err := s.adminFlag()
	if err != nil {
		return authz.Authority{}, err
	}
	return authz.NewUserAuthority(authz.UserID(s.UserID), admin)
}

// adminFlag validates the subject's role strings and reports whether it holds the
// admin role. Only the known catalog roles are accepted; an unknown role fails
// closed.
func (s Subject) adminFlag() (bool, error) {
	admin := false
	for _, r := range s.Roles {
		switch r {
		case RoleAdmin:
			admin = true
		case RoleUser:
			// ordinary user — the default even with no role at all.
		default:
			return false, fmt.Errorf("auth: unknown role %q", r)
		}
	}
	return admin, nil
}

// ChannelAuthority mints a UserActor for a dedicated channel turn. channelID is
// read from the persisted channel configuration by the channel adapter; it is
// never request payload identity. The exact binding is consumed only by the
// Agent PEP's dedicated-channel decision.
func (s Subject) ChannelAuthority(channelID string) (authz.Authority, error) {
	admin, err := s.adminFlag()
	if err != nil {
		return authz.Authority{}, err
	}
	return authz.NewChannelAuthority(authz.UserID(s.UserID), admin, channelID)
}
