package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

// --- Auth User Management API (admin-only) ---

// authUserResponse is the response shape for auth user endpoints.
type authUserResponse struct {
	ID         string                 `json:"id"`
	Email      string                 `json:"email"`
	Name       string                 `json:"name"`
	Role       string                 `json:"role"`
	IsActive   bool                   `json:"is_active"`
	Identities []auth.ChannelIdentity `json:"identities"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

func (s *Server) buildAuthUserResponse(r *http.Request, u auth.User) (authUserResponse, error) {
	var identities []auth.ChannelIdentity
	if s.users != nil {
		var err error
		identities, err = s.users.ListChannelIdentitiesByUser(r.Context(), u.ID)
		if err != nil {
			return authUserResponse{}, fmt.Errorf("list identities: %w", err)
		}
	}

	return authUserResponse{
		ID:         u.ID,
		Email:      u.Email,
		Name:       u.Name,
		Role:       u.Role,
		IsActive:   u.IsActive,
		Identities: identities,
		CreatedAt:  u.CreatedAt.UTC(),
		UpdatedAt:  u.UpdatedAt.UTC(),
	}, nil
}

// ListAuthUsers handles GET /api/auth/users.
func (s *Server) ListAuthUsers(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	users, err := s.users.ListUsers(r.Context())
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

	writeData(w, http.StatusOK, map[string]any{"users": result})
}

// GetAuthUser handles GET /api/auth/users/{id}.
func (s *Server) GetAuthUser(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	s.writeAuthUser(w, r, id)
}

// writeAuthUser loads the auth user by ID and writes the full AuthUser resource.
func (s *Server) writeAuthUser(w http.ResponseWriter, r *http.Request, id string) {
	u, err := s.users.GetUser(r.Context(), id)
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

// UpdateAuthUserRole handles PATCH /api/auth/users/{id}/role.
func (s *Server) UpdateAuthUserRole(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
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

	if info.UserID == id && body.Role != auth.RoleAdmin {
		writeError(w, http.StatusBadRequest, "cannot remove your own admin role")
		return
	}

	if err := s.users.UpdateUserRole(r.Context(), id, body.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role: "+err.Error())
		return
	}

	// Force re-authentication so new role takes effect in tokens.
	if s.sessions != nil {
		_ = s.sessions.DeleteUserSessions(r.Context(), id)
	}

	s.writeAuthUser(w, r, id)
}

// ListAuthUserAgents handles GET /api/auth/users/{id}/agents.
func (s *Server) ListAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	agentIDs, err := s.authStore.ListUserAgentIDs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list user agents: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]any{"agent_ids": agentIDs})
}

// UpdateAuthUserAgents handles PATCH /api/auth/users/{id}/agents.
func (s *Server) UpdateAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
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
	if _, err := s.users.GetUser(r.Context(), id); err != nil {
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

	updated, err := s.authStore.ListUserAgentIDs(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list user agents: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"agent_ids": updated})
}

// DeleteAuthUserIdentity handles DELETE /api/auth/users/{id}/identities/{identityId}.
func (s *Server) DeleteAuthUserIdentity(w http.ResponseWriter, r *http.Request, id string, identityId string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if s.users == nil {
		writeError(w, http.StatusServiceUnavailable, "channel identity store not configured")
		return
	}
	ctx := r.Context()

	// Verify the identity exists and belongs to the specified user.
	identity, err := s.users.GetChannelIdentity(ctx, identityId)
	if err != nil {
		writeError(w, http.StatusNotFound, "identity not found")
		return
	}

	if identity.UserID != id {
		writeError(w, http.StatusBadRequest, "identity does not belong to this user")
		return
	}

	if err := s.users.DeleteChannelIdentity(ctx, identityId); err != nil {
		s.log.Error("admin delete identity", "id", identityId, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete identity")
		return
	}

	writeNoContent(w)
}

// UpdateAuthUserActive handles PATCH /api/auth/users/{id}/active.
func (s *Server) UpdateAuthUserActive(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if info.UserID == id && !body.IsActive {
		writeError(w, http.StatusBadRequest, "cannot deactivate your own account")
		return
	}

	if _, err := s.users.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if err := s.users.UpdateUserActive(r.Context(), id, body.IsActive); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update active status: "+err.Error())
		return
	}

	// If deactivating, delete all their sessions to force logout.
	if !body.IsActive && s.sessions != nil {
		_ = s.sessions.DeleteUserSessions(r.Context(), id)
	}

	s.writeAuthUser(w, r, id)
}

// ListAuthUserLoginIdentities handles GET /api/auth/users/{id}/identities/login.
func (s *Server) ListAuthUserLoginIdentities(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if s.logins == nil {
		writeData(w, http.StatusOK, map[string]any{"identities": []auth.LoginIdentity{}})
		return
	}
	if _, err := s.users.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	identities, err := s.logins.ListLoginIdentitiesByUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list login identities: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"identities": identities})
}

// LinkAuthUserLoginIdentity handles POST /api/auth/users/{id}/identities/login.
func (s *Server) LinkAuthUserLoginIdentity(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if s.logins == nil {
		writeError(w, http.StatusServiceUnavailable, "login identity store not configured")
		return
	}

	var body struct {
		Provider        string `json:"provider"`
		ProviderSubject string `json:"provider_subject"`
		Email           string `json:"email"`
		Name            string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Provider == "" || body.ProviderSubject == "" || body.Email == "" {
		writeError(w, http.StatusBadRequest, "provider, provider_subject, and email are required")
		return
	}

	if _, err := s.users.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	// Ensure the identity is not already owned by another user.
	existing, err := s.logins.GetLoginIdentityByProvider(r.Context(), body.Provider, body.ProviderSubject)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to check existing identity: "+err.Error())
		return
	}
	if err == nil && existing.UserID != id {
		writeError(w, http.StatusConflict, "identity is already linked to another user")
		return
	}
	if err == nil && existing.UserID == id {
		writeData(w, http.StatusOK, existing)
		return
	}

	linked, err := s.logins.CreateLoginIdentity(r.Context(), auth.LoginIdentity{
		ID:              uuid.NewString(),
		UserID:          id,
		Provider:        body.Provider,
		ProviderSubject: body.ProviderSubject,
		Email:           body.Email,
		Name:            body.Name,
	})
	if err != nil {
		s.log.Error("admin link login identity", "user_id", id, "provider", body.Provider, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to link identity: "+err.Error())
		return
	}

	s.log.Info("admin linked login identity", "user_id", id, "provider", body.Provider, "subject", body.ProviderSubject)
	writeData(w, http.StatusOK, linked)
}

// ListAuthUserChannelIdentities handles GET /api/auth/users/{id}/identities/channel.
func (s *Server) ListAuthUserChannelIdentities(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if _, err := s.users.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if s.users == nil {
		writeData(w, http.StatusOK, map[string]any{"identities": []auth.ChannelIdentity{}})
		return
	}
	identities, err := s.users.ListChannelIdentitiesByUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channel identities: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"identities": identities})
}
