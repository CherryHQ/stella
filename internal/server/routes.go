package server

import (
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
	apiserver.HandlerFromMux(s, s.mux)
}

func (s *Server) registerStaticRoutes() {
	s.mux.Handle("GET /static/", web.StaticHandler())
	s.mux.HandleFunc("GET /{$}", s.redirectRoot)
	// OIDC browser redirect flow — not OpenAPI routes.
	s.mux.HandleFunc("GET /auth/login/{provider}", s.handleOIDCLogin)
	s.mux.HandleFunc("GET /auth/callback/{provider}", s.handleOIDCCallback)
}

func (s *Server) registerPageRoutes() {
	s.mux.Handle("GET /{path...}", web.SPAHandler())
}
