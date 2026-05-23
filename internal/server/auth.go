package server

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/vault"
)

// Register handles POST /api/auth/register.
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	// Rate limit registration by IP.
	ip := clientIP(r)
	if err := s.rateLimiter.CheckIP(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(body.Username) > 64 {
		writeError(w, http.StatusBadRequest, "username must be at most 64 characters")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if len(body.Password) > 72 {
		writeError(w, http.StatusBadRequest, "password must be at most 72 characters")
		return
	}

	ctx := r.Context()

	// Check if this will be the first user BEFORE creating, to avoid race.
	count, err := s.authStore.CountUsers(ctx)
	if err != nil {
		s.log.Error("count users", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	isFirstUser := count == 0

	// Hash password.
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.log.Error("hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Create user.
	user, err := s.authStore.CreateUser(ctx, body.Username, hash)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			s.rateLimiter.RecordIPAttempt(ip)
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		s.log.Error("create user", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// First user gets admin role.
	if isFirstUser {
		_ = s.authStore.UpdateUserRole(ctx, user.ID, auth.RoleAdmin)
	}

	// Generate vault keys if the master recipient is configured.
	if s.vaultRecipient != nil {
		pubKey, encPrivKey, err := vault.GenerateUserKeys(s.vaultRecipient)
		if err != nil {
			s.log.Warn("generate age keys failed", "user_id", user.ID, "error", err)
		} else if err := s.authStore.UpdateUserAgeKeys(ctx, user.ID, pubKey, encPrivKey); err != nil {
			s.log.Warn("store age keys failed", "user_id", user.ID, "error", err)
		}
	}

	// Create session.
	sessionID := auth.NewSessionID()
	_, err = s.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		s.log.Error("create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	secure := !isLocalhost(r)
	auth.SetSessionCookie(w, sessionID, secure)

	writeData(w, http.StatusCreated, map[string]any{
		"id":       user.ID,
		"username": user.Username,
	})
}

// Login handles POST /api/auth/login.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	body.Username = strings.TrimSpace(body.Username)

	// Rate limit by IP.
	ip := clientIP(r)
	if err := s.rateLimiter.CheckIP(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	// Rate limit by username.
	if body.Username != "" {
		if err := s.rateLimiter.CheckUsername(body.Username); err != nil {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
	}

	ctx := r.Context()

	user, err := s.authStore.GetUserByUsername(ctx, body.Username)
	if err != nil {
		s.rateLimiter.RecordIPAttempt(ip)
		s.rateLimiter.RecordLoginFailure(body.Username)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, body.Password); err != nil {
		s.rateLimiter.RecordIPAttempt(ip)
		s.rateLimiter.RecordLoginFailure(body.Username)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if !user.IsActive {
		writeError(w, http.StatusForbidden, "account is deactivated")
		return
	}

	s.rateLimiter.RecordLoginSuccess(body.Username)

	// Create session.
	sessionID := auth.NewSessionID()
	_, err = s.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		s.log.Error("create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	secure := !isLocalhost(r)
	auth.SetSessionCookie(w, sessionID, secure)

	writeData(w, http.StatusOK, map[string]any{
		"id":       user.ID,
		"username": user.Username,
	})
}

// Logout handles POST /api/auth/logout.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	rawToken, err := auth.GetSessionCookie(r)
	if err == nil {
		ctx := r.Context()
		// Revoke OIDC session (hash-based) if configured.
		if s.authSvc != nil {
			_ = s.authSvc.Logout(ctx, rawToken)
		} else {
			// Legacy session: cookie value is the raw session ID.
			_ = s.authStore.DeleteSession(ctx, rawToken)
		}
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
	writeData(w, http.StatusOK, map[string]any{
		"id":       info.UserID,
		"username": info.Username,
		"role":     info.Role,
		"is_admin": info.IsAdmin,
	})
}

func isLocalhost(r *http.Request) bool {
	host := r.Host
	hostOnly, _, err := net.SplitHostPort(host)
	if err != nil {
		hostOnly = host
	}
	if ip := net.ParseIP(hostOnly); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return strings.HasPrefix(hostOnly, "localhost")
}

// clientIP extracts the client IP from the request, checking X-Forwarded-For
// and X-Real-IP headers first.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// Strip port from RemoteAddr.
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}
