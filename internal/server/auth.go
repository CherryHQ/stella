package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/auth"
)

// Logout handles POST /api/auth/logout.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	rawToken, err := auth.GetSessionCookie(r)
	if err == nil && s.authSvc != nil {
		_ = s.authSvc.Logout(r.Context(), rawToken)
	}
	auth.ClearSessionCookie(w)
	writeNoContent(w)
}

// GetMe handles GET /api/auth/me.
func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	resp := map[string]any{
		"id":       info.UserID,
		"username": info.Username,
		"role":     info.Role,
		"is_admin": info.IsAdmin,
	}
	if info.Email != "" {
		resp["email"] = info.Email
	}
	if info.Name != "" {
		resp["name"] = info.Name
	}
	if info.AvatarURL != "" {
		resp["avatar_url"] = info.AvatarURL
	}
	if info.OrgID != "" {
		resp["org_id"] = info.OrgID
		if s.organizations != nil {
			if org, err := s.organizations.GetOrganization(r.Context(), info.OrgID); err == nil {
				resp["org_name"] = org.Name
			}
		}
	}
	if s.credentials != nil {
		_, err := s.credentials.GetCredentialByUserID(r.Context(), info.UserID)
		resp["has_credentials"] = err == nil
	}
	writeData(w, http.StatusOK, resp)
}

// UpdateOrg handles PATCH /api/auth/org. Returns the updated organization.
func (s *Server) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	if info.OrgID == "" {
		writeError(w, http.StatusBadRequest, "no organization")
		return
	}
	if s.organizations == nil {
		writeError(w, http.StatusServiceUnavailable, "organization store unavailable")
		return
	}
	var req apiserver.UpdateOrgJSONRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.organizations.UpdateOrganizationName(r.Context(), info.OrgID, req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	org, err := s.organizations.GetOrganization(r.Context(), info.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, org)
}

// ListAuthSessions handles GET /api/auth/sessions.
func (s *Server) ListAuthSessions(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "session store unavailable")
		return
	}
	sessions, err := s.sessions.ListSessionsByUser(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, len(sessions))
	for i, sess := range sessions {
		items[i] = map[string]any{
			"id":         sess.ID,
			"expires_at": sess.ExpiresAt,
			"created_at": sess.CreatedAt,
		}
	}
	writeListData(w, http.StatusOK, items)
}

// DeleteAuthSession handles DELETE /api/auth/sessions/{id}.
func (s *Server) DeleteAuthSession(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "session store unavailable")
		return
	}
	sess, err := s.sessions.GetSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sess.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "not your session")
		return
	}
	if err := s.sessions.DeleteSession(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}
