package server

import (
	"errors"
	"net/http"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
)

// authority builds the trusted UserActor Authority for an authenticated HTTP
// caller. The mint happens inside internal/auth; the server never constructs an
// Authority from request fields. IsAdmin is the credential/session gate's
// effective privilege, so a bearer for an admin account remains non-admin.
func (info *AuthInfo) authority() (authz.Authority, error) {
	role := auth.RoleUser
	if info.IsAdmin {
		role = auth.RoleAdmin
	}
	return auth.Subject{UserID: info.UserID, Roles: []string{role}}.Authority()
}

// agentAccessError maps an agentaccess typed error to an HTTP status and message,
// preserving the accepted 404-not-found / 403-forbidden split (an authenticated
// denial is 403; a missing agent or a hidden-visibility denial is 404).
func agentAccessError(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, agentaccess.ErrNotFound):
		return http.StatusNotFound, "agent not found"
	case errors.Is(err, agentaccess.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}
