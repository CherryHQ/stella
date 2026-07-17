package server

import (
	"errors"
	"net/http"
	"time"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	memprofile "github.com/CherryHQ/stella/internal/memory/profile"
)

// profileMemoryResponse is the JSON shape for a (user, agent) memory resource. It
// preserves the historical ctx_agent_memory field set, names, and order; the
// Profile domain value supplies the decoded constraints/entries.
type profileMemoryResponse struct {
	UserID         string                   `json:"user_id"`
	AgentID        string                   `json:"agent_id"`
	Content        string                   `json:"content"`
	Soul           string                   `json:"soul"`
	Version        int64                    `json:"version"`
	Constraints    []memory.ConstraintEntry `json:"constraints"`
	ProfileEntries []memory.ProfileEntry    `json:"profile_entries"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

func profileMemoryResponseFrom(m memprofile.Memory) profileMemoryResponse {
	return profileMemoryResponse{
		UserID:         m.UserID,
		AgentID:        m.AgentID,
		Content:        m.Content,
		Soul:           m.Soul,
		Version:        m.Version,
		Constraints:    m.Constraints,
		ProfileEntries: m.ProfileEntries,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

// profileError maps a Profile service error to its HTTP status and message,
// preserving the historical bodies. Agent-gate denials reuse the agent access
// mapping (404 "agent not found" / 403 "forbidden"); anything unrecognized is a
// logged 500.
func profileError(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, memprofile.ErrConstraintNotFound):
		return http.StatusNotFound, "constraint not found"
	case errors.Is(err, memprofile.ErrProfileStoreUnavailable):
		return http.StatusServiceUnavailable, "profile memory store not configured"
	case errors.Is(err, memprofile.ErrChangelogReaderUnavailable):
		return http.StatusServiceUnavailable, "memory changelog reader not configured"
	case errors.Is(err, agentaccess.ErrNotFound), errors.Is(err, agentaccess.ErrForbidden):
		return agentAccessError(err)
	case errors.Is(err, authz.ErrNotFound):
		return http.StatusNotFound, "user not found"
	case errors.Is(err, authz.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// profileAuthority resolves the trusted Authority for a self profile request. It
// writes the 401/403 response and returns ok=false when the caller is not
// authenticated. The Profile service owns the Agent-access gate from here.
func (s *Server) profileAuthority(w http.ResponseWriter, r *http.Request) (authz.Authority, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return authz.Authority{}, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return authz.Authority{}, false
	}
	return authority, true
}

// writeProfileError writes the mapped profile error; a 500 is logged through
// writeInternalError.
func (s *Server) writeProfileError(w http.ResponseWriter, err error) {
	code, msg := profileError(err)
	if code == http.StatusInternalServerError {
		s.writeInternalError(w, err)
		return
	}
	writeError(w, code, msg)
}
