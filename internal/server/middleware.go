package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

// contextKey is used for storing auth info in request context.
type contextKey string

const authInfoKey contextKey = "authInfo"

// AuthInfo carries authenticated user data through request context.
type AuthInfo struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IsAdmin  bool   `json:"is_admin"`
	// OIDC principal fields; empty for legacy (password-based) sessions.
	Email     string `json:"email,omitempty"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	OrgID     string `json:"org_id,omitempty"`
}

// UserFromContext extracts the AuthInfo from a request context.
// Returns nil if the user is not authenticated.
func UserFromContext(ctx context.Context) *AuthInfo {
	info, _ := ctx.Value(authInfoKey).(*AuthInfo)
	return info
}

// withAuthInfo sets the AuthInfo in the request context.
func withAuthInfo(ctx context.Context, info *AuthInfo) context.Context {
	return context.WithValue(ctx, authInfoKey, info)
}

// authMiddleware validates the session cookie, loads the user and roles,
// and injects AuthInfo into the request context. Unauthenticated requests
// to API routes get 401; page routes get redirected to /login.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Exempt paths: login page, static assets, public shares, auth endpoints, OAuth callbacks.
		if path == "/login" ||
			strings.HasPrefix(path, "/assets/") ||
			strings.HasPrefix(path, "/static/") ||
			strings.HasPrefix(path, "/s/") ||
			(r.Method == http.MethodGet && strings.HasPrefix(path, "/api/shares/public/")) ||
			path == "/api/auth/login" ||
			path == "/api/auth/register" ||
			path == "/api/auth/logout" ||
			path == "/api/auth/providers" ||
			strings.HasPrefix(path, "/auth/login/") ||
			strings.HasPrefix(path, "/auth/callback/") ||
			(strings.HasPrefix(path, "/api/auth/profile/oauth/") && strings.HasSuffix(path, "/callback")) ||
			localOIDCExempt(path) {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		if info := s.authInfoFromBearer(ctx, r.Header.Get("Authorization")); info != nil {
			next.ServeHTTP(w, r.WithContext(withAuthInfo(ctx, info)))
			return
		}

		// Try OIDC session (new auth_session table, token-hash lookup).
		if info := s.authInfoFromOIDCSession(ctx, r); info != nil {
			next.ServeHTTP(w, r.WithContext(withAuthInfo(ctx, info)))
			return
		}

		// Try to load session.
		sessionID, err := auth.GetSessionCookie(r)
		if err != nil {
			if path == "/api/status" {
				next.ServeHTTP(w, r)
				return
			}
			s.denyAccess(w, r)
			return
		}

		// Clean up expired sessions lazily.
		_ = s.authStore.DeleteExpiredSessions(ctx)

		session, err := s.authStore.GetSession(ctx, sessionID)
		if err != nil {
			if path == "/api/status" {
				next.ServeHTTP(w, r)
				return
			}
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
		}

		// Check expiry.
		if time.Now().After(session.ExpiresAt) {
			if path == "/api/status" {
				next.ServeHTTP(w, r)
				return
			}
			_ = s.authStore.DeleteSession(ctx, sessionID)
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
		}

		// Extend session expiry on each authenticated request.
		_ = s.authStore.UpdateSessionExpiry(ctx, sessionID, time.Now().Add(auth.SessionDuration))

		// Load user.
		user, err := s.authStore.GetUser(ctx, session.UserID)
		if err != nil {
			if path == "/api/status" {
				next.ServeHTTP(w, r)
				return
			}
			_ = s.authStore.DeleteSession(ctx, sessionID)
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
		}

		if !user.IsActive {
			if path == "/api/status" {
				next.ServeHTTP(w, r)
				return
			}
			_ = s.authStore.DeleteSession(ctx, sessionID)
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
		}

		info := &AuthInfo{
			UserID:   user.ID,
			Username: user.Username,
			Role:     user.Role,
			IsAdmin:  user.IsAdmin(),
		}

		next.ServeHTTP(w, r.WithContext(withAuthInfo(ctx, info)))
	})
}

func (s *Server) authInfoFromOIDCSession(ctx context.Context, r *http.Request) *AuthInfo {
	if s.sessionMgr == nil || s.authSvc == nil {
		return nil
	}
	rawToken, err := s.sessionMgr.GetToken(r)
	if err != nil {
		return nil
	}
	principal, err := s.authSvc.PrincipalFromToken(ctx, rawToken)
	if err != nil {
		return nil
	}
	return &AuthInfo{
		UserID:    principal.UserID,
		Username:  principal.Email,
		Role:      principal.Role,
		IsAdmin:   principal.Role == "admin",
		Email:     principal.Email,
		Name:      principal.Name,
		AvatarURL: principal.AvatarURL,
		OrgID:     principal.OrgID,
	}
}

func (s *Server) authInfoFromBearer(ctx context.Context, header string) *AuthInfo {
	if s.tokenSvc == nil {
		return nil
	}
	scheme, rawToken, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(rawToken) == "" {
		return nil
	}
	user, err := s.tokenSvc.Authenticate(ctx, rawToken)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("bearer token auth failed", "error", err)
		}
		return nil
	}
	// When OIDC is configured, bearer tokens must honor OIDC membership state
	// and role; legacy auth_users.role may be stale after membership changes.
	if s.authSvc != nil {
		membership, err := s.authSvc.GetUserMembership(ctx, user.ID)
		if err != nil || !membership.IsActive {
			return nil
		}
		return &AuthInfo{
			UserID:   user.ID,
			Username: user.Username,
			Role:     membership.Role,
			IsAdmin:  membership.Role == auth.RoleAdmin,
			OrgID:    membership.OrganizationID,
		}
	}
	return &AuthInfo{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		IsAdmin:  user.IsAdmin(),
	}
}

// requireAdmin checks that the authenticated user has the admin role.
// It writes a 403 JSON error and returns false when the caller is not admin.
// Returns true when the caller is admin. Intended for inline use in API handlers.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	info := UserFromContext(r.Context())
	if info == nil || !info.IsAdmin {
		writeError(w, http.StatusForbidden, "admin access required")
		return false
	}
	return true
}

// denyAccess returns 401 for API routes or redirects to /login for page routes.
func (s *Server) denyAccess(w http.ResponseWriter, r *http.Request) {
	if isAPIRoute(r.URL.Path) {
		writeError(w, http.StatusUnauthorized, "authentication required")
	} else {
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// isAPIRoute returns true if the path starts with /api/.
func isAPIRoute(path string) bool {
	return strings.HasPrefix(path, "/api/")
}
