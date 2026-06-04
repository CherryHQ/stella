package server

import (
	"net/http"
	"strings"

	"github.com/CherryHQ/stella/internal/auth"
)

func scopedTokenAllowsRequest(claims *auth.ScopedTokenClaims, r *http.Request) bool {
	if claims == nil {
		return true
	}
	if claims.AgentID == "" {
		return false
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "api" {
		return false
	}
	if len(parts) >= 3 && parts[1] == "agents" {
		return parts[2] == claims.AgentID
	}
	switch parts[1] {
	case "tasks", "goals", "shares", "status":
		return true
	default:
		return false
	}
}
