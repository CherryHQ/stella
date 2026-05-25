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
	writeData(w, http.StatusOK, map[string]string{"status": "logged out"})
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
	}
	writeData(w, http.StatusOK, resp)
}
