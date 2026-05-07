package server

import (
	"fmt"
	"net/http"

	"github.com/vaayne/anna/internal/auth"
)

// --- Auth User Management API (admin-only) ---

// authUserResponse is the response shape for auth user endpoints.
type authUserResponse struct {
	ID         int64           `json:"id"`
	Username   string          `json:"username"`
	Role       string          `json:"role"`
	IsActive   bool            `json:"is_active"`
	Identities []auth.Identity `json:"identities"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

func (s *Server) buildAuthUserResponse(r *http.Request, u auth.AuthUser) (authUserResponse, error) {
	identities, err := s.authStore.ListIdentitiesByUser(r.Context(), u.ID)
	if err != nil {
		return authUserResponse{}, fmt.Errorf("list identities: %w", err)
	}

	return authUserResponse{
		ID:         u.ID,
		Username:   u.Username,
		Role:       u.Role,
		IsActive:   u.IsActive,
		Identities: identities,
		CreatedAt:  u.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:  u.UpdatedAt.Format("2006-01-02 15:04:05"),
	}, nil
}

// ListAuthUsers handles GET /api/auth/users.
func (s *Server) ListAuthUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
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

// GetAuthUser handles GET /api/auth/users/{id}.
func (s *Server) GetAuthUser(w http.ResponseWriter, r *http.Request, id int64) {
	if !requireAdmin(w, r) {
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

// UpdateAuthUserRole handles PUT /api/auth/users/{id}/role.
func (s *Server) UpdateAuthUserRole(w http.ResponseWriter, r *http.Request, id int64) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if body.Role != auth.RoleAdmin && body.Role != auth.RoleUser {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'user'")
		return
	}

	// Cannot demote yourself.
	info := UserFromContext(r.Context())
	if info != nil && info.UserID == id && body.Role != auth.RoleAdmin {
		writeError(w, http.StatusBadRequest, "cannot remove your own admin role")
		return
	}

	if err := s.authStore.UpdateUserRole(r.Context(), id, body.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ListAuthUserAgents handles GET /api/auth/users/{id}/agents.
func (s *Server) ListAuthUserAgents(w http.ResponseWriter, r *http.Request, id int64) {
	if !requireAdmin(w, r) {
		return
	}
	agentIDs, err := s.authStore.ListUserAgentIDs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list user agents: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, agentIDs)
}

// UpdateAuthUserAgents handles PUT /api/auth/users/{id}/agents.
func (s *Server) UpdateAuthUserAgents(w http.ResponseWriter, r *http.Request, id int64) {
	if !requireAdmin(w, r) {
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

// DeleteAuthUserIdentity handles DELETE /api/auth/users/{id}/identities/{identityId}.
func (s *Server) DeleteAuthUserIdentity(w http.ResponseWriter, r *http.Request, id int64, identityId int64) {
	if !requireAdmin(w, r) {
		return
	}
	ctx := r.Context()

	// Verify the identity exists.
	identity, err := s.authStore.GetIdentity(ctx, identityId)
	if err != nil {
		writeError(w, http.StatusNotFound, "identity not found")
		return
	}

	// Verify it belongs to the specified user.
	if identity.UserID != id {
		writeError(w, http.StatusBadRequest, "identity does not belong to this user")
		return
	}

	if err := s.authStore.DeleteIdentity(ctx, identityId); err != nil {
		s.log.Error("admin delete identity", "id", identityId, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete identity")
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "unlinked"})
}

// UpdateAuthUserActive handles PUT /api/auth/users/{id}/active.
func (s *Server) UpdateAuthUserActive(w http.ResponseWriter, r *http.Request, id int64) {
	if !requireAdmin(w, r) {
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
