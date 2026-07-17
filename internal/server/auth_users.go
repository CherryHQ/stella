package server

import (
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/account"
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

// authUserResponseFromView encodes an Account domain view into the auth-user
// resource shape. Identities are never nil so the JSON always carries an array.
func authUserResponseFromView(v account.AccountView) authUserResponse {
	identities := v.Identities
	if identities == nil {
		identities = []auth.ChannelIdentity{}
	}
	return authUserResponse{
		ID:         v.User.ID,
		Email:      v.User.Email,
		Name:       v.User.Name,
		Role:       v.User.Role,
		IsActive:   v.User.IsActive,
		Identities: identities,
		CreatedAt:  v.User.CreatedAt.UTC(),
		UpdatedAt:  v.User.UpdatedAt.UTC(),
	}
}

// ListAuthUsers handles GET /api/users.
func (s *Server) ListAuthUsers(w http.ResponseWriter, r *http.Request, params apiserver.ListAuthUsersParams) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	views, err := s.account.ListUsers(r.Context(), authority, int64(limit+1), int64(offset))
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	page, nextToken := nextPageTokenForRows(views, limit, offset)

	result := make([]authUserResponse, 0, len(page))
	for _, v := range page {
		result = append(result, authUserResponseFromView(v))
	}

	out := map[string]any{"users": result}
	if nextToken != "" {
		out["next_page_token"] = nextToken
	}
	writeData(w, http.StatusOK, out)
}

// GetAuthUser handles GET /api/users/{id}.
func (s *Server) GetAuthUser(w http.ResponseWriter, r *http.Request, id string) {
	info, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	view, err := s.account.GetUser(r.Context(), authority, targetUserID)
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, authUserResponseFromView(view))
}

// UpdateAuthUserRole handles PATCH /api/users/{id}/role.
func (s *Server) UpdateAuthUserRole(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	view, err := s.account.UpdateRole(r.Context(), authority, targetUserID, body.Role)
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, authUserResponseFromView(view))
}

// ListAuthUserAgents handles GET /api/users/{id}/agents.
func (s *Server) ListAuthUserAgents(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	agentIDs, err := s.account.ListUserAgents(r.Context(), authority, targetUserID)
	if err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	var body struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	updated, err := s.account.SetUserAgents(r.Context(), authority, targetUserID, body.AgentIDs)
	if err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	if err := s.account.DeleteUserChannelIdentity(r.Context(), authority, targetUserID, identityId); err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	view, err := s.account.SetActive(r.Context(), authority, targetUserID, body.IsActive)
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, authUserResponseFromView(view))
}

// ListAuthUserLoginIdentities handles GET /api/users/{id}/identities/login.
func (s *Server) ListAuthUserLoginIdentities(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	identities, err := s.account.ListLoginIdentities(r.Context(), authority, targetUserID)
	if err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)

	var body struct {
		Provider        string `json:"provider"`
		ProviderSubject string `json:"provider_subject"`
		Email           string `json:"email"`
		Name            string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Provider == "" || body.ProviderSubject == "" || body.Email == "" {
		writeError(w, http.StatusBadRequest, "provider, provider_subject, and email are required")
		return
	}

	linked, err := s.account.LinkLoginIdentity(r.Context(), authority, targetUserID, account.LinkLoginInput{
		Provider:        body.Provider,
		ProviderSubject: body.ProviderSubject,
		Email:           body.Email,
		Name:            body.Name,
	})
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, linked)
}

// ListAuthUserChannelIdentities handles GET /api/users/{id}/identities/channel.
func (s *Server) ListAuthUserChannelIdentities(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	targetUserID := resolveTargetUserID(info, id)
	identities, err := s.account.ListChannelIdentities(r.Context(), authority, targetUserID)
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"identities": identities})
}
