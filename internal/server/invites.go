package server

import (
	"fmt"
	"net/http"
	"time"
)

const inviteCookieName = "stella_invite"

// ListInvites handles GET /api/auth/invites (admin only).
func (s *Server) ListInvites(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	invites, err := s.authSvc.ListOrgInvites(r.Context(), info.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeListData(w, http.StatusOK, invites)
}

// CreateInvite handles POST /api/auth/invites (admin only).
func (s *Server) CreateInvite(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}

	var req struct {
		Email    string `json:"email"`
		Role     string `json:"role"`
		MaxUses  int    `json:"max_uses"`
		TTLHours int    `json:"ttl_hours"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if req.MaxUses < 1 {
		req.MaxUses = 1
	}
	if req.TTLHours < 1 {
		req.TTLHours = 168 // 7 days
	}

	rawToken, invite, err := s.authSvc.CreateInvite(
		r.Context(),
		info.OrgID,
		req.Email,
		req.Role,
		info.UserID,
		req.MaxUses,
		time.Duration(req.TTLHours)*time.Hour,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	base := s.baseURL
	if base == "" {
		base = requestBaseURL(r)
	}
	inviteURL := fmt.Sprintf("%s/auth/invite/%s", base, rawToken)
	writeData(w, http.StatusCreated, map[string]any{
		"invite":     invite,
		"invite_url": inviteURL,
	})
}

// RevokeInvite handles DELETE /api/auth/invites/{id} (admin only).
func (s *Server) RevokeInvite(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if err := s.authSvc.RevokeInvite(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}

// GetInviteInfo handles GET /api/auth/invites/{token}/info (public).
func (s *Server) GetInviteInfo(w http.ResponseWriter, r *http.Request, token string) {
	inv, org, err := s.authSvc.GetInviteInfo(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invite not found or expired")
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"invite":   inv,
		"org_name": org.Name,
	})
}

// AcceptInvite handles POST /api/auth/invites/{token}/accept (authenticated).
func (s *Server) AcceptInvite(w http.ResponseWriter, r *http.Request, token string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if err := s.authSvc.RedeemInvite(r.Context(), token, info.UserID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeNoContent(w)
}

// handleInviteRedirect handles GET /invite/{token}.
// If the user has an active session, redirects to the SPA accept page.
// Otherwise sets an invite cookie and redirects to /login.
func (s *Server) handleInviteRedirect(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	if s.authSvc == nil {
		http.Error(w, "invites not available", http.StatusServiceUnavailable)
		return
	}

	_, _, err := s.authSvc.GetInviteInfo(r.Context(), token)
	if err != nil {
		http.Error(w, "invite not found or expired", http.StatusNotFound)
		return
	}

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	if info := s.authInfoFromOIDCSession(r.Context(), r); info != nil {
		if info.NeedsOnboarding {
			setInviteCookie(w, token, secure)
			http.Redirect(w, r, "/onboarding", http.StatusFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/auth/invite/%s/accept", token), http.StatusFound)
		return
	}

	setInviteCookie(w, token, secure)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func setInviteCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     inviteCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   900, // 15 minutes
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func readInviteCookie(r *http.Request) string {
	c, err := r.Cookie(inviteCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func clearInviteCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     inviteCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
