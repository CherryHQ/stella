package server

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/web"
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
	// authz data (kind and scopes). nil for cookie/OIDC sessions (which skip
	// API-scope enforcement).
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
			s.log.WarnContext(ctx, "bearer credential rejected", "error", err, "path", path)
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

		// Bearer credentials carry a credential kind and go through the unified
		// entry gate. PATs inherit account authority; OAuth tokens additionally
		// enforce delegated scopes. Cookie/OIDC sessions have no kind and skip this
		// gate. /api/status is public, so a narrowly scoped OAuth bearer must not get
		// a 403 where an anonymous caller gets 200.
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

// requireInteractiveAdmin is the sole gate for lifecycle actions that mint or
// manage bearer credentials. A bearer may resolve to an admin account, but it
// is not an interactive session and must never create its own replacement.
func requireInteractiveAdmin(w http.ResponseWriter, r *http.Request) *AuthInfo {
	info := requireAuth(w, r)
	if info == nil {
		return nil
	}
	if info.principal != nil {
		writeError(w, http.StatusForbidden, "interactive admin session required")
		return nil
	}
	if !info.IsAdmin {
		writeError(w, http.StatusForbidden, "admin access required")
		return nil
	}
	return info
}

// requireProvisioningBearer is the one entry gate for the provisioned-user
// family. A session or a personal/admin PAT may have broad account authority,
// but neither is the deliberately constrained provisioning capability.
func requireProvisioningBearer(w http.ResponseWriter, r *http.Request) *AuthInfo {
	info := requireAuth(w, r)
	if info == nil {
		return nil
	}
	if info.principal == nil || info.principal.Kind != credential.KindProvisioning || info.principal.CredentialID == "" {
		writeError(w, http.StatusForbidden, "provisioning token required")
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
	case slices.Contains(web.PWARootFiles, path):
		// The progressive-web-app manifest, worker, and icons. A browser fetches
		// them with no session — redirect them to /login and it rejects the
		// manifest and the worker on their content type, so the Web UI never
		// becomes installable.
		return true
	case path == "/healthz" || path == "/readyz":
		// Infrastructure probes for orchestrators (kubelet); no user session.
		return true
	case strings.HasPrefix(path, "/assets/") ||
		strings.HasPrefix(path, "/static/") ||
		strings.HasPrefix(path, "/api-references") ||
		strings.HasPrefix(path, "/s/"):
		return true
	case (method == http.MethodGet || method == http.MethodHead) && strings.HasPrefix(path, "/api/shares/public/"):
		return true
	case strings.HasPrefix(path, "/auth/login/") || strings.HasPrefix(path, "/auth/callback/"):
		return true
	case method == http.MethodGet && path == "/api/mcp/oauth/callback":
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

// accessLogMiddleware emits one log line per completed request. It sits inside
// the OTel wrap — the request context carries a live span, so lines get a
// trace_id when tracing is enabled — and outside authMiddleware so denied
// requests (401/redirect) are logged too. That placement means the
// authenticated user is not available here; correlate by trace_id instead.
func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case isQuietPath(r.URL.Path):
			level = slog.LevelDebug
		}

		// trace_id/span_id come from the trace-context slog handler (main installs
		// it), which reads them off the request context passed here.
		s.log.LogAttrs(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
			slog.Int64("bytes", rec.bytes),
		)
	})
}

// isQuietPath returns true for high-frequency, low-signal requests
// (orchestrator probes and static assets) that log at DEBUG instead of INFO.
func isQuietPath(path string) bool {
	return path == "/healthz" || path == "/readyz" ||
		strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/static/")
}

// statusRecorder captures the response status and body size for the access
// log. Flush is forwarded so SSE handlers, which assert w.(http.Flusher),
// keep streaming; Unwrap supports http.ResponseController.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
