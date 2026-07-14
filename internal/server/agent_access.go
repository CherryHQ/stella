package server

import (
	"errors"
	"net/http"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
)

// authority builds the trusted UserActor Authority for an authenticated HTTP
// caller from its verified session claims. The mint happens inside internal/auth
// (the trusted producer of a session subject); the server never constructs an
// Authority from request-supplied path/body fields. Roles come from the verified
// session role exactly as the legacy subject did.
func (info *AuthInfo) authority() (authz.Authority, error) {
	role := info.Role
	if role == "" {
		role = auth.RoleUser
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
