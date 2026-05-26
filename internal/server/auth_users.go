package server

import (
	"errors"
	"fmt"
	"net/http"

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
	CreatedAt  string                 `json:"created_at"`
	UpdatedAt  string                 `json:"updated_at"`
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

	role := auth.RoleUser
	isActive := u.IsActive
	if s.memberships != nil {
		if m, err := s.memberships.GetUserMembership(r.Context(), u.ID); err == nil {
			role = m.Role
			isActive = m.IsActive
		}
	}

	return authUserResponse{
		ID:         u.ID,
		Email:      u.Email,
		Name:       u.Name,
		Role:       role,
		IsActive:   isActive,
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

	writeData(w, http.StatusOK, result)
}

// GetAuthUser handles GET /api/auth/users/{id}.
func (s *Server) GetAuthUser(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
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

	if s.memberships == nil {
		writeError(w, http.StatusServiceUnavailable, "membership store not configured")
		return
	}

	m, err := s.memberships.GetUserMembership(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "membership not found for user")
		return
	}

	if err := s.memberships.UpdateMembershipRole(r.Context(), m.ID, body.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role: "+err.Error())
		return
	}

	writeNoContent(w)
}

// ListAuthUserAgents handles GET /api/auth/users/{id}/agents.
func (s *Server) ListAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
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

// UpdateAuthUserAgents handles PATCH /api/auth/users/{id}/agents.
func (s *Server) UpdateAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
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

	writeNoContent(w)
}

// DeleteAuthUserIdentity handles DELETE /api/auth/users/{id}/identities/{identityId}.
func (s *Server) DeleteAuthUserIdentity(w http.ResponseWriter, r *http.Request, id string, identityId string) {
	if !requireAdmin(w, r) {
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

	if _, err := s.users.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if s.memberships != nil {
		if m, err := s.memberships.GetUserMembership(r.Context(), id); err == nil {
			_ = s.memberships.UpdateMembershipActive(r.Context(), m.ID, body.IsActive)
		}
	}

	// If deactivating, delete all their sessions to force logout.
	if !body.IsActive && s.sessions != nil {
		_ = s.sessions.DeleteUserSessions(r.Context(), id)
	}

	writeNoContent(w)
}

// ListAuthUserLoginIdentities handles GET /api/auth/users/{id}/identities/login.
func (s *Server) ListAuthUserLoginIdentities(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	if s.logins == nil {
		writeData(w, http.StatusOK, []auth.LoginIdentity{})
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
	writeData(w, http.StatusOK, identities)
}

// LinkAuthUserLoginIdentity handles POST /api/auth/users/{id}/identities/login.
func (s *Server) LinkAuthUserLoginIdentity(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
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
	if !requireAdmin(w, r) {
		return
	}
	if _, err := s.users.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if s.users == nil {
		writeData(w, http.StatusOK, []auth.ChannelIdentity{})
		return
	}
	identities, err := s.users.ListChannelIdentitiesByUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channel identities: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, identities)
}
