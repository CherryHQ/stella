package server

import (
	"errors"
	"net/http"

	"github.com/CherryHQ/stella/internal/auth/account"
)

// accountError maps an Account service typed error to its HTTP status and client
// message, preserving the historical bodies. Anything unrecognized is a logged
// 500 (see writeAccountError).
func accountError(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, account.ErrUserNotFound):
		return http.StatusNotFound, "user not found"
	case errors.Is(err, account.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, account.ErrInvalidRole):
		return http.StatusBadRequest, "role must be 'admin' or 'user'"
	case errors.Is(err, account.ErrSelfRoleRemoval):
		return http.StatusBadRequest, "cannot remove your own admin role"
	case errors.Is(err, account.ErrSelfDeactivate):
		return http.StatusBadRequest, "cannot deactivate your own account"
	case errors.Is(err, account.ErrIdentityNotFound):
		return http.StatusNotFound, "identity not found"
	case errors.Is(err, account.ErrIdentityNotOwnedByTarget):
		return http.StatusBadRequest, "identity does not belong to this user"
	case errors.Is(err, account.ErrIdentityNotOwnedBySelf):
		return http.StatusForbidden, "identity does not belong to you"
	case errors.Is(err, account.ErrIdentityConflict):
		return http.StatusConflict, "identity is already linked to another user"
	case errors.Is(err, account.ErrSessionNotFound):
		return http.StatusNotFound, "session not found"
	case errors.Is(err, account.ErrSessionForeign):
		return http.StatusForbidden, "not your session"
	case errors.Is(err, account.ErrPasswordIncorrect):
		return http.StatusUnauthorized, "current password is incorrect"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// writeAccountError writes the mapped account error. A 500 is routed through
// writeInternalError so the underlying cause is logged.
func (s *Server) writeAccountError(w http.ResponseWriter, err error) {
	code, msg := accountError(err)
	if code == http.StatusInternalServerError {
		s.writeInternalError(w, err)
		return
	}
	writeError(w, code, msg)
}
