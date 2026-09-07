package server

import (
	"errors"
	"net/http"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/mcp"
)

func writePluginOAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mcp.ErrOAuthClientInitializationRequired),
		errors.Is(err, authz.ErrForbidden), errors.Is(err, authz.ErrNotFound),
		errors.Is(err, agentaccess.ErrForbidden), errors.Is(err, agentaccess.ErrNotFound):
		writePluginError(w, err)
	default:
		writeError(w, http.StatusBadRequest, "OAuth authorization could not be completed")
	}
}
