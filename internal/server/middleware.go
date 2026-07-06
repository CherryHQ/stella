package server

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/credential"
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
	// principal is the resolved bearer credential and the single carrier of its
	// authz data (kind, scopes, and the scoped-token agent/session binding). nil
	// for cookie/OIDC sessions (which skip API-scope enforcement).
	principal *credential.Principal
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

		if isAuthExempt(r.Method, path) {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()

		// A present-but-invalid bearer credential is a hard deny: it must never
		// fall through to the cookie session or any full-access path.
		info, err := s.authInfoFromBearer(ctx, r.Header.Get("Authorization"))
		if err != nil {
			s.log.Warn("bearer credential rejected", "error", err, "path", path)
			s.denyAccess(w, r)
			return
		}
		if info == nil {
			info = s.authInfoFromOIDCSession(ctx, r)
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

		// Scoped bearers (PAT / OAuth) carry a credential kind and go through the
		// unified enforcement gate. Cookie/OIDC sessions
		// have no kind and skip API-scope enforcement (handler ownership/admin
		// checks still apply to them). /api/status is a public health endpoint
		// (reachable anonymously above), so a valid but narrowly-scoped bearer
		// must not get a 403 there where an anonymous caller gets 200.
		if info.principal != nil && path != "/api/status" {
			if err := credential.Enforce(info.principal, r.Method, path); err != nil {
				writeError(w, http.StatusForbidden, "permission denied")
				return
			}
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
		IsAdmin:   principal.IsAdmin(),
		Email:     principal.Email,
		Name:      principal.Name,
		AvatarURL: principal.AvatarURL,
	}
}

// authInfoFromBearer resolves an Authorization bearer through the unified
// credential front door. It returns:
//   - (nil, nil)  when there is no bearer to resolve (fall back to cookie auth);
//   - (nil, err)  when a bearer is present but invalid/unknown/reserved (deny);
//   - (info, nil) on success.
func (s *Server) authInfoFromBearer(ctx context.Context, header string) (*AuthInfo, error) {
	if s.credResolver == nil {
		return nil, nil
	}
	p, err := s.credResolver.Resolve(ctx, header)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	info := &AuthInfo{
		UserID:    p.UserID,
		Username:  p.Username,
		Email:     p.Email,
		Name:      p.Name,
		AvatarURL: p.AvatarURL,
		Role:      p.Role,
		IsAdmin:   p.IsAdmin,
		principal: p,
	}
	return info, nil
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

// Public auth API paths that don't require a session.
var publicAuthAPIPaths = []string{
	"/api/auth/logout",
	"/api/auth/providers",
	"/api/auth/local/login",
	"/api/auth/local/register",
}

// isAuthExempt returns true for paths that bypass session validation:
// login/signup pages, static assets, public shares, auth flow endpoints, and OAuth callbacks.
func isAuthExempt(method, path string) bool {
	switch {
	case path == "/login" || path == "/signup":
		return true
	case strings.HasPrefix(path, "/assets/") ||
		strings.HasPrefix(path, "/static/") ||
		strings.HasPrefix(path, "/api-references") ||
		strings.HasPrefix(path, "/s/"):
		return true
	case method == http.MethodGet && strings.HasPrefix(path, "/api/shares/public/"):
		return true
	case strings.HasPrefix(path, "/auth/login/") || strings.HasPrefix(path, "/auth/callback/"):
		return true
	case method == http.MethodPost && path == "/oauth/token":
		// OAuth2 token endpoint authenticates the client itself; it must NOT
		// require a Stella user session. /oauth/authorize is deliberately NOT
		// exempt -- it needs a logged-in user to render consent.
		return true
	case strings.HasPrefix(path, "/api/auth/"):
		if slices.Contains(publicAuthAPIPaths, path) {
			return true
		}
		return strings.HasSuffix(path, "/callback") &&
			(strings.HasPrefix(path, "/api/auth/oauth/") || strings.HasPrefix(path, "/api/auth/profile/oauth/"))
	}
	return false
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
