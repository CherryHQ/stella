package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/web"
)

func (s *Server) registerRoutes() {
	s.registerStaticRoutes()
	s.registerPageRoutes()
	s.registerAPIRoutes()
}

// registerAPIRoutes mounts all REST API routes onto the admin mux.
// Auth is enforced by the global authMiddleware (Bearer + session).
func (s *Server) registerAPIRoutes() {
	apiserver.HandlerWithOptions(s, apiserver.StdHTTPServerOptions{
		BaseRouter: s.mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadRequest, "invalid request parameters")
		},
	})
	s.mux.HandleFunc("GET /api/{path...}", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "api route not found")
	})
}

func (s *Server) registerStaticRoutes() {
	s.mux.Handle("GET /static/", web.StaticHandler())
	s.mux.HandleFunc("GET /{$}", s.redirectRoot)
	// Kubernetes-style infrastructure probes. These are intentionally NOT in the
	// OpenAPI spec (api/CLAUDE.md spec-first workflow): they are unversioned infra
	// endpoints for orchestrators, not part of the product API contract. Auth is
	// bypassed via isAuthExempt so a kubelet can reach them without a session.
	s.mux.HandleFunc("GET /healthz", s.readiness.healthz)
	s.mux.HandleFunc("GET /readyz", s.readiness.readyz)
	// API documentation (Scalar UI).
	s.mux.HandleFunc("GET /api-references", s.handleDocsPage)
	s.mux.HandleFunc("GET /api-references/openapi.yaml", s.handleDocsSpec)
	// OIDC browser redirect flow — not OpenAPI routes.
	s.mux.HandleFunc("GET /auth/login/{provider}", s.handleOIDCLogin)
	s.mux.HandleFunc("GET /auth/callback/{provider}", s.handleOIDCCallback)
	// OAuth2 authorization server (issue #613) — protocol wire endpoints, not
	// OpenAPI/JSON API. /oauth/authorize needs a Stella session (consent);
	// /oauth/token authenticates the client and is auth-exempt (middleware.go).
	s.mux.HandleFunc("GET /oauth/authorize", s.handleOAuthAuthorize)
	s.mux.HandleFunc("POST /oauth/authorize", s.handleOAuthAuthorize)
	s.mux.HandleFunc("POST /oauth/token", s.handleOAuthToken)
	// The inbound webhook capability ingress is NOT registered here: it is a
	// capability-authenticated trigger whose opaque URL capability is the sole
	// credential (Authorization is ignored) and must bypass the admin middleware
	// chain, so the composition root mounts it behind the capability reservation
	// on the HTTP root ahead of the admin mux.
}

func (s *Server) registerPageRoutes() {
	s.mux.Handle("GET /{path...}", web.SPAHandler())
}
