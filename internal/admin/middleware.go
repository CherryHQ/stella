package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/auth"
)

// contextKey is used for storing auth info in request context.
type contextKey string

const authInfoKey contextKey = "authInfo"

// AuthInfo carries authenticated user data through request context.
type AuthInfo struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IsAdmin  bool   `json:"is_admin"`
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

		// Exempt paths: login page, static assets, auth endpoints, OAuth callbacks.
		if path == "/login" ||
			strings.HasPrefix(path, "/static/") ||
			path == "/api/auth/login" ||
			path == "/api/auth/register" ||
			path == "/api/auth/logout" ||
			path == "/api/auth/profile/oauth/lark/callback" {
			next.ServeHTTP(w, r)
			return
		}

		// Try to load session.
		sessionID, err := auth.GetSessionCookie(r)
		if err != nil {
			s.denyAccess(w, r)
			return
		}

		ctx := r.Context()

		// Clean up expired sessions lazily.
		_ = s.authStore.DeleteExpiredSessions(ctx)

		session, err := s.authStore.GetSession(ctx, sessionID)
		if err != nil {
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
		}

		// Check expiry.
		if time.Now().After(session.ExpiresAt) {
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
			_ = s.authStore.DeleteSession(ctx, sessionID)
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
		}

		if !user.IsActive {
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

// adminOnlyMiddleware checks that the authenticated user has the admin role.
// Returns 403 for non-admin users.
func (s *Server) adminOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := UserFromContext(r.Context())
		if info == nil || !info.IsAdmin {
			if isAPIRoute(r.URL.Path) {
				writeError(w, http.StatusForbidden, "admin access required")
			} else {
				http.Redirect(w, r, "/agents", http.StatusFound)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
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
