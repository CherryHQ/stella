package server

import (
	"net/http"
	"strings"

	"github.com/CherryHQ/stella/internal/observability"
)

const webhookCapabilityPath = "/webhooks/:capability"

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

// WebhookIngressHandler returns the auth-exempt inbound webhook ingress handler
// (POST /webhooks/{capability}). The composition root mounts it at the HTTP root,
// in front of the admin middleware chain, because the URL is a bearer capability
// rather than a Stella session credential. It stays in internal/server because it
// only orchestrates transport/session work over the injected webhook domain.
func (s *Server) WebhookIngressHandler() http.Handler {
	return s.accessLogMiddleware(http.HandlerFunc(s.handleWebhookIngress))
}

// RedactWebhookCapability removes the raw URL bearer capability before a request
// reaches observability or logging. ServeMux has already selected the route when
// this wrapper runs, so PathValue remains available to the handler; clone and
// restore it explicitly to make that contract robust across request wrappers.
// Query parameters remain available through URL.Query, but RequestURI is made
// deliberately opaque too: traces and access logs must never see the token.
func RedactWebhookCapability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capability := r.PathValue("capability")
		if capability == "" && !strings.HasPrefix(r.URL.Path, "/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = webhookCapabilityPath
		clone.URL.RawPath = webhookCapabilityPath
		clone.RequestURI = webhookCapabilityPath
		clone.SetPathValue("capability", capability)
		next.ServeHTTP(w, clone)
	})
}

// ObservedWebhookIngressHandler applies the root-boundary redaction before both
// OTel and access logging. It is separate from Handler because this public route
// bypasses the admin auth/CORS/JSON middleware chain.
func (s *Server) ObservedWebhookIngressHandler() http.Handler {
	return RedactWebhookCapability(observability.Handler(s.WebhookIngressHandler()))
}

// NewRootMux constructs the production root handler. Every /webhooks/ subtree
// shape reaches the redaction boundary before it can fall through to the admin
// OTel/access-log chain; only a canonical POST single-segment path reaches
// ingress, and every other shape fails closed.
func NewRootMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST /webhooks/{capability}", s.ObservedWebhookIngressHandler())
	mux.Handle("/webhooks/{path...}", RedactWebhookCapability(http.NotFoundHandler()))
	mux.Handle("/", s.Handler())
	return mux
}

// Handler returns the HTTP handler with OTel instrumentation wrapping the
// access-log, CORS, JSON, and auth middleware chain. The OTel wrap is
// unconditional: it is a no-op when tracing is disabled. The access log sits
// directly inside it so log lines can carry the request's trace_id.
func (s *Server) Handler() http.Handler {
	return observability.Handler(s.accessLogMiddleware(s.corsMiddleware(s.authMiddleware(s.jsonMiddleware(s.mux)))))
}

// corsMiddleware handles CORS headers. Origin is read from settings at startup.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.baseURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
