package server

import (
	"errors"
	"net/http"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/providercred"
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

// agentManagementError maps an Agent management typed error to an HTTP status and
// message. It extends agentAccessError with the write-path validation/lookup
// errors, preserving the historical bodies: an invalid scope is 400, a missing
// assignment target is 404 "user not found", a missing agent is 404 "agent not
// found", an authenticated denial is 403, and anything else is a logged 500.
func agentManagementError(err error) (int, string) {
	switch {
	case errors.Is(err, agentaccess.ErrUnknownProvider),
		errors.Is(err, providercred.ErrEmptyProviderID),
		errors.Is(err, providercred.ErrEmptyAPIKey),
		errors.Is(err, providercred.ErrDuplicateProvider),
		errors.Is(err, providercred.ErrTooManyCredentials),
		errors.Is(err, providercred.ErrProviderIDTooLong),
		errors.Is(err, providercred.ErrAPIKeyTooLong):
		return http.StatusBadRequest, "invalid provider credential"
	case errors.Is(err, agentaccess.ErrCredentialsUnavailable):
		return http.StatusServiceUnavailable, "provider credentials are unavailable"
	case errors.Is(err, agentaccess.ErrInvalidScope):
		return http.StatusBadRequest, "scope must be 'system' or 'restricted'"
	case errors.Is(err, agentaccess.ErrUserNotFound):
		return http.StatusNotFound, "user not found"
	case errors.Is(err, agentaccess.ErrInUse):
		return http.StatusConflict, "agent is still used by a webhook"
	default:
		return agentAccessError(err)
	}
}
