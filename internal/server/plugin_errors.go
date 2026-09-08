package server

import (
	"errors"
	"net/http"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

// errPluginCapabilityUnavailable is retained for the MCP adapter while the
// file-backed resource service is being wired through the composition root.
var errPluginCapabilityUnavailable = errors.New("plugin backend capability unavailable")

func writePluginError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "internal error"
	switch {
	case errors.Is(err, pluginpkg.ErrConflict), errors.Is(err, mcp.ErrVersionConflict):
		status, message = http.StatusConflict, "resource revision conflict"
	case isUniqueViolation(err):
		status, message = http.StatusConflict, "resource already exists"
	case errors.Is(err, pluginpkg.ErrBuiltinConfig):
		status, message = http.StatusConflict, "builtin system configuration cannot be deleted"
	case errors.Is(err, pluginpkg.ErrUnknownScope), errors.Is(err, pluginpkg.ErrInvalidConfig), errors.Is(err, pluginpkg.ErrInvalidDefinition), errors.Is(err, pluginpkg.ErrInvalidResourceID), errors.Is(err, pluginpkg.ErrResourceLimit):
		status, message = http.StatusBadRequest, "invalid plugin request"
	case errors.Is(err, mcp.ErrOAuthClientInitializationRequired):
		status, message = http.StatusConflict, "administrator must initialize this connection before users can authorize their own accounts"
	case errors.Is(err, agentaccess.ErrForbidden), errors.Is(err, authz.ErrForbidden), errors.Is(err, pluginpkg.ErrForbidden):
		status, message = http.StatusForbidden, "forbidden"
	case errors.Is(err, agentaccess.ErrNotFound), errors.Is(err, authz.ErrNotFound), errors.Is(err, pluginpkg.ErrNotFound):
		status, message = http.StatusNotFound, "not found"
	case errors.Is(err, errPluginCapabilityUnavailable):
		status, message = http.StatusServiceUnavailable, "plugin backend capability unavailable"
	}
	writeError(w, status, message)
}
