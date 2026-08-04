package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/observability"
	"github.com/CherryHQ/stella/internal/version"
)

// redirectRoot sends unauthenticated users to /login, admins to /providers,
// and regular users to /agents.
func (s *Server) redirectRoot(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if info.IsAdmin {
		http.Redirect(w, r, "/providers", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/agents", http.StatusFound)
}

// WebhookIngressHandler returns the capability-authenticated inbound webhook
// ingress handler. The composition root mounts it behind the capability
// reservation at the HTTP root, in front of the admin middleware chain, because
// the opaque URL capability (not a session or Authorization header) is the sole
// credential: the webhook module resolves and revalidates the resource's fixed
// owner→Agent authority, so any Authorization header is ignored and the handler
// must never be wrapped by the session authMiddleware. It stays in
// internal/server, with its Server-field dependencies, because it is transport
// code — not a plugin implementation.
func (s *Server) WebhookIngressHandler() http.Handler {
	return s.accessLogMiddleware(http.HandlerFunc(s.handleWebhookIngress))
}

// Handler returns the HTTP handler with OTel instrumentation wrapping the
// access-log, CORS, JSON, and auth middleware chain. The OTel wrap is
// unconditional: it is a no-op when tracing is disabled. The access log sits
// directly inside it so log lines can carry the request's trace_id.
func (s *Server) Handler() http.Handler {
	return observability.Handler(s.accessLogMiddleware(s.corsMiddleware(buildMiddleware(s.authMiddleware(s.jsonMiddleware(s.mux))))))
}

// BuildHeader names the build serving a response. A single-page app never
// re-fetches its document on its own, so an open tab can run a superseded
// bundle for days; stamping every response lets the tab notice an upgrade from
// traffic it already makes, instead of polling an endpoint on a timer.
const BuildHeader = "X-Stella-Build"

// buildMiddleware stamps BuildHeader on every response. It sits outside
// authMiddleware so a 401 or a redirect carries it too — a tab whose session
// lapsed while the server was upgraded still learns it is stale. The value is
// already public through GET /api/status, so this discloses nothing new.
func buildMiddleware(next http.Handler) http.Handler {
	build := version.Version
	if version.Commit != "" {
		build += "@" + version.Commit
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(BuildHeader, build)
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware handles CORS headers. Origin is read from settings at startup.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.baseURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		// Without this a cross-origin Web UI cannot read BuildHeader at all, and
		// the upgrade notice silently never fires.
		w.Header().Set("Access-Control-Expose-Headers", BuildHeader)
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// jsonMiddleware sets JSON content-type for /api/ routes.
func (s *Server) jsonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}
