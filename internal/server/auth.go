package server

import (
	"net/http"

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
	if s.credentials != nil {
		_, err := s.credentials.GetCredentialByUserID(r.Context(), info.UserID)
		resp["has_credentials"] = err == nil
	}
	writeData(w, http.StatusOK, resp)
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
		s.writeInternalError(w, err)
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
	writeData(w, http.StatusOK, map[string]any{"sessions": items})
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
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}
