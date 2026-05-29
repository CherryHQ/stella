package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

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
			strings.HasPrefix(path, "/api-references") ||
			strings.HasPrefix(path, "/assets/") ||
			strings.HasPrefix(path, "/static/") ||
			strings.HasPrefix(path, "/s/") ||
			(r.Method == http.MethodGet && strings.HasPrefix(path, "/api/shares/public/")) ||
			path == "/api/auth/logout" ||
			path == "/api/auth/providers" ||
			strings.HasPrefix(path, "/auth/login/") ||
			strings.HasPrefix(path, "/auth/callback/") ||
			(strings.HasPrefix(path, "/api/auth/profile/oauth/") && strings.HasSuffix(path, "/callback")) ||
			strings.HasPrefix(path, "/oidc/local/") {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()

		var info *AuthInfo
		if i := s.authInfoFromBearer(ctx, r.Header.Get("Authorization")); i != nil {
			info = i
		} else if i := s.authInfoFromOIDCSession(ctx, r); i != nil {
			info = i
		}

		if info == nil {
			if path == "/api/status" {
				next.ServeHTTP(w, r)
				return
			}
			auth.ClearSessionCookie(w)
			s.denyAccess(w, r)
			return
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
		Role:      RoleAdmin,
		IsAdmin:   true,
		Email:     principal.Email,
		Name:      principal.Name,
		AvatarURL: principal.AvatarURL,
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
	return &AuthInfo{
		UserID:    user.ID,
		Username:  user.Email,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      RoleAdmin,
		IsAdmin:   true,
	}
}

// requireAuth extracts the authenticated user from the request context.
// Returns nil and writes a 401 error if the user is not authenticated.
func requireAuth(w http.ResponseWriter, r *http.Request) *AuthInfo {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil
	}
	return info
}

// requireAdmin checks that the authenticated user has the admin role.
// Returns nil and writes a 403 error if the user is not admin.
func requireAdmin(w http.ResponseWriter, r *http.Request) *AuthInfo {
	info := requireAuth(w, r)
	if info == nil {
		return nil
	}
	if !info.IsAdmin {
		writeError(w, http.StatusForbidden, "admin access required")
		return nil
	}
	return info
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

// RoleAdmin is the admin role constant used in single-tenant mode.
const RoleAdmin = "admin"
