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
		if parts[2] != claims.AgentID {
			return false
		}
		return claims.HasScope(agentPathScope(r.Method, parts[3:]))
	}
	if scope := topLevelPathScope(r.Method, parts[1:]); scope != "" {
		return claims.HasScope(scope)
	}
	return false
}

func agentPathScope(method string, rest []string) string {
	if len(rest) == 0 {
		return readWriteScope("agent", method)
	}
	switch rest[0] {
	case "scheduler":
		return readWriteScope("scheduler", method)
	case "skills":
		return readWriteScope("skills", method)
	case "tasks":
		return readWriteScope("tasks", method)
	case "sessions":
		return readWriteScope("agent", method)
	default:
		return readWriteScope("agent", method)
	}
}

func topLevelPathScope(method string, rest []string) string {
	if len(rest) == 0 {
		return ""
	}
	switch rest[0] {
	case "status":
		return "agent:read"
	case "tasks":
		return readWriteScope("tasks", method)
	case "goals":
		return readWriteScope("goals", method)
	case "shares":
		return readWriteScope("shares", method)
	case "articles", "feeds", "digests", "recally":
		return readWriteScope("recally", method)
	case "oauth":
		return readWriteScope("oauth", method)
	case "vault":
		return readWriteScope("vault", method)
	default:
		return ""
	}
}

func readWriteScope(resource string, method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return resource + ":read"
	default:
		return resource + ":write"
	}
}
