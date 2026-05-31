package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
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

// ListAuthUsers handles GET /api/users.
func (s *Server) ListAuthUsers(w http.ResponseWriter, r *http.Request, params apiserver.ListAuthUsersParams) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	users, err := s.users.ListUsersPaged(r.Context(), int64(limit+1), int64(offset))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	page, nextToken := nextPageTokenForRows(users, limit, offset)

	result := make([]authUserResponse, 0, len(page))
	for _, u := range page {
		resp, err := s.buildAuthUserResponse(r, u)
		if err != nil {
			s.log.Error("build auth user response", "user_id", u.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list users")
			return
		}
		result = append(result, resp)
	}

	out := map[string]any{"users": result}
	if nextToken != "" {
		out["next_page_token"] = nextToken
	}
	writeData(w, http.StatusOK, out)
}

// GetAuthUser handles GET /api/users/{id}.
func (s *Server) GetAuthUser(w http.ResponseWriter, r *http.Request, id string) {
	_, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	s.writeAuthUser(w, r, targetUserID)
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

// UpdateAuthUserRole handles PATCH /api/users/{id}/role.
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

	targetUserID := resolveTargetUserID(info, id)
	if info.UserID == targetUserID && body.Role != auth.RoleAdmin {
		writeError(w, http.StatusBadRequest, "cannot remove your own admin role")
		return
	}

	if err := s.users.UpdateUserRole(r.Context(), targetUserID, body.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role: "+err.Error())
		return
	}

	// Force re-authentication so new role takes effect in tokens.
	if s.sessions != nil {
		_ = s.sessions.DeleteUserSessions(r.Context(), targetUserID)
	}

	s.writeAuthUser(w, r, targetUserID)
}

// ListAuthUserAgents handles GET /api/users/{id}/agents.
func (s *Server) ListAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	agentIDs, err := s.authStore.ListUserAgentIDs(r.Context(), targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list user agents: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]any{"agent_ids": agentIDs})
}

// UpdateAuthUserAgents handles PATCH /api/users/{id}/agents.
func (s *Server) UpdateAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	var body struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Verify user exists.
	if _, err := s.users.GetUser(r.Context(), targetUserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	ctx := r.Context()

	// Get current assignments.
	currentIDs, err := s.authStore.ListUserAgentIDs(ctx, targetUserID)
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
			if err := s.authStore.RemoveAgent(ctx, targetUserID, aid); err != nil {
				s.log.Error("remove agent assignment", "user_id", targetUserID, "agent_id", aid, "error", err)
			}
		}
	}

	// Add agents not in current set.
	for _, aid := range body.AgentIDs {
		if !currentSet[aid] {
			if err := s.authStore.AssignAgent(ctx, targetUserID, aid); err != nil {
				s.log.Error("assign agent", "user_id", targetUserID, "agent_id", aid, "error", err)
			}
		}
	}

	updated, err := s.authStore.ListUserAgentIDs(ctx, targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list user agents: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"agent_ids": updated})
}

// DeleteAuthUserIdentity handles DELETE /api/users/{id}/identities/{identityId}.
func (s *Server) DeleteAuthUserIdentity(w http.ResponseWriter, r *http.Request, id string, identityId string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
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

	if identity.UserID != targetUserID {
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

// UpdateAuthUserActive handles PATCH /api/users/{id}/active.
func (s *Server) UpdateAuthUserActive(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if info.UserID == targetUserID && !body.IsActive {
		writeError(w, http.StatusBadRequest, "cannot deactivate your own account")
		return
	}

	if _, err := s.users.GetUser(r.Context(), targetUserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if err := s.users.UpdateUserActive(r.Context(), targetUserID, body.IsActive); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update active status: "+err.Error())
		return
	}

	// If deactivating, delete all their sessions to force logout.
	if !body.IsActive && s.sessions != nil {
		_ = s.sessions.DeleteUserSessions(r.Context(), targetUserID)
	}

	s.writeAuthUser(w, r, targetUserID)
}

// ListAuthUserLoginIdentities handles GET /api/users/{id}/identities/login.
func (s *Server) ListAuthUserLoginIdentities(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	if s.logins == nil {
		writeData(w, http.StatusOK, map[string]any{"identities": []auth.LoginIdentity{}})
		return
	}
	if _, err := s.users.GetUser(r.Context(), targetUserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	identities, err := s.logins.ListLoginIdentitiesByUser(r.Context(), targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list login identities: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"identities": identities})
}

// LinkAuthUserLoginIdentity handles POST /api/users/{id}/identities/login.
func (s *Server) LinkAuthUserLoginIdentity(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
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

	if _, err := s.users.GetUser(r.Context(), targetUserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	// Ensure the identity is not already owned by another user.
	existing, err := s.logins.GetLoginIdentityByProvider(r.Context(), body.Provider, body.ProviderSubject)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to check existing identity: "+err.Error())
		return
	}
	if err == nil && existing.UserID != targetUserID {
		writeError(w, http.StatusConflict, "identity is already linked to another user")
		return
	}
	if err == nil && existing.UserID == targetUserID {
		writeData(w, http.StatusOK, existing)
		return
	}

	linked, err := s.logins.CreateLoginIdentity(r.Context(), auth.LoginIdentity{
		ID:              uuid.NewString(),
		UserID:          targetUserID,
		Provider:        body.Provider,
		ProviderSubject: body.ProviderSubject,
		Email:           body.Email,
		Name:            body.Name,
	})
	if err != nil {
		s.log.Error("admin link login identity", "user_id", targetUserID, "provider", body.Provider, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to link identity: "+err.Error())
		return
	}

	s.log.Info("admin linked login identity", "user_id", targetUserID, "provider", body.Provider, "subject", body.ProviderSubject)
	writeData(w, http.StatusOK, linked)
}

// ListAuthUserChannelIdentities handles GET /api/users/{id}/identities/channel.
func (s *Server) ListAuthUserChannelIdentities(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	if _, err := s.users.GetUser(r.Context(), targetUserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if s.users == nil {
		writeData(w, http.StatusOK, map[string]any{"identities": []auth.ChannelIdentity{}})
		return
	}
	identities, err := s.users.ListChannelIdentitiesByUser(r.Context(), targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channel identities: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"identities": identities})
}
