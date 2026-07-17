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
	resp["has_credentials"] = s.account.HasPassword(r.Context(), info.UserID)
	writeData(w, http.StatusOK, resp)
}

// ListAuthSessions handles GET /api/auth/sessions.
func (s *Server) ListAuthSessions(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	sessions, err := s.account.ListSessions(r.Context(), authority)
	if err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := s.account.DeleteSession(r.Context(), authority, id); err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeNoContent(w)
}
