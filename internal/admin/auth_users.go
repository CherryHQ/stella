package admin

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/vaayne/anna/internal/auth"
)

// --- Auth User Management API (admin-only) ---

// authUserResponse is the response shape for auth user endpoints.
type authUserResponse struct {
	ID         int64           `json:"id"`
	Username   string          `json:"username"`
	IsActive   bool            `json:"is_active"`
	Roles      []string        `json:"roles"`
	Identities []auth.Identity `json:"identities"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

func (s *Server) buildAuthUserResponse(r *http.Request, u auth.AuthUser) (authUserResponse, error) {
	ctx := r.Context()

	roles, err := s.authStore.ListUserRoles(ctx, u.ID)
	if err != nil {
		return authUserResponse{}, fmt.Errorf("list roles: %w", err)
	}
	roleIDs := make([]string, len(roles))
	for i, role := range roles {
		roleIDs[i] = role.ID
	}

	identities, err := s.authStore.ListIdentitiesByUser(ctx, u.ID)
	if err != nil {
		return authUserResponse{}, fmt.Errorf("list identities: %w", err)
	}

	return authUserResponse{
		ID:         u.ID,
		Username:   u.Username,
		IsActive:   u.IsActive,
		Roles:      roleIDs,
		Identities: identities,
		CreatedAt:  u.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:  u.UpdatedAt.Format("2006-01-02 15:04:05"),
	}, nil
}

// listAuthUsers handles GET /api/auth/users.
func (s *Server) listAuthUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.authStore.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users: "+err.Error())
		return
	}

	result := make([]authUserResponse, 0, len(users))
	for _, u := range users {
		resp, err := s.buildAuthUserResponse(r, u)
		if err != nil {
			s.log.Error("build auth user response", "user_id", u.ID, "error", err)
			continue
		}
		result = append(result, resp)
	}

	writeData(w, http.StatusOK, result)
}

// getAuthUser handles GET /api/auth/users/{id}.
func (s *Server) getAuthUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	u, err := s.authStore.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	resp, err := s.buildAuthUserResponse(r, u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user details: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, resp)
}

// updateAuthUserRoles handles PUT /api/auth/users/{id}/roles.
func (s *Server) updateAuthUserRoles(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var body struct {
		Role   string `json:"role"`
		Action string `json:"action"` // "assign" or "remove"
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if body.Role == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}
	if body.Action != "assign" && body.Action != "remove" {
		writeError(w, http.StatusBadRequest, "action must be 'assign' or 'remove'")
		return
	}

	// Cannot remove own admin role.
	info := UserFromContext(r.Context())
	if info != nil && info.UserID == id && body.Role == auth.RoleAdmin && body.Action == "remove" {
		writeError(w, http.StatusBadRequest, "cannot remove your own admin role")
		return
	}

	// Verify user exists.
	if _, err := s.authStore.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	// Verify role exists.
	if _, err := s.authStore.GetRole(r.Context(), body.Role); err != nil {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}

	ctx := r.Context()
	if body.Action == "assign" {
		if err := s.authStore.AssignRole(ctx, id, body.Role); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to assign role: "+err.Error())
			return
		}
	} else {
		if err := s.authStore.RemoveRole(ctx, id, body.Role); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to remove role: "+err.Error())
			return
		}
	}

	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

// listAuthUserAgents handles GET /api/auth/users/{id}/agents.
func (s *Server) listAuthUserAgents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	agentIDs, err := s.authStore.ListUserAgentIDs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list user agents: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, agentIDs)
}

// updateAuthUserAgents handles PUT /api/auth/users/{id}/agents.
func (s *Server) updateAuthUserAgents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var body struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Verify user exists.
	if _, err := s.authStore.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	ctx := r.Context()

	// Get current assignments.
	currentIDs, err := s.authStore.ListUserAgentIDs(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list current agents: "+err.Error())
		return
	}

	currentSet := make(map[string]bool, len(currentIDs))
	for _, aid := range currentIDs {
		currentSet[aid] = true
	}
	desiredSet := make(map[string]bool, len(body.AgentIDs))
	for _, aid := range body.AgentIDs {
		desiredSet[aid] = true
	}

	// Remove agents not in desired set.
	for _, aid := range currentIDs {
		if !desiredSet[aid] {
			if err := s.authStore.RemoveAgent(ctx, id, aid); err != nil {
				s.log.Error("remove agent assignment", "user_id", id, "agent_id", aid, "error", err)
			}
		}
	}

	// Add agents not in current set.
	for _, aid := range body.AgentIDs {
		if !currentSet[aid] {
			if err := s.authStore.AssignAgent(ctx, id, aid); err != nil {
				s.log.Error("assign agent", "user_id", id, "agent_id", aid, "error", err)
			}
		}
	}

	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

// updateAuthUserActive handles PUT /api/auth/users/{id}/active.
func (s *Server) updateAuthUserActive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Cannot deactivate yourself.
	info := UserFromContext(r.Context())
	if info != nil && info.UserID == id && !body.IsActive {
		writeError(w, http.StatusBadRequest, "cannot deactivate your own account")
		return
	}

	u, err := s.authStore.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	u.IsActive = body.IsActive
	if err := s.authStore.UpdateUser(r.Context(), u); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update user: "+err.Error())
		return
	}

	// If deactivating, delete all their sessions to force logout.
	if !body.IsActive {
		_ = s.authStore.DeleteUserSessions(r.Context(), id)
	}

	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}
