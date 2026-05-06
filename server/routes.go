package server

import (
	"net/http"

	apiserver "github.com/vaayne/anna/api/server"
	"github.com/vaayne/anna/web"
)

func (s *Server) registerRoutes() {
	s.registerStaticRoutes()
	s.registerPageRoutes()
	s.registerPluginRoutes()
	s.registerManifestPluginRoutes()
	s.registerAPIRoutes()
}

// registerAPIRoutes mounts the generated recally + scheduler REST API onto the
// admin mux. Auth is enforced by the global authMiddleware (Bearer + session).
func (s *Server) registerAPIRoutes() {
	apiserver.HandlerFromMux(s, s.mux)
}

func (s *Server) registerStaticRoutes() {
	s.mux.Handle("GET /static/", web.StaticHandler())
	s.mux.HandleFunc("GET /login", s.pageLogin)
	s.mux.HandleFunc("GET /{$}", s.redirectRoot)
}

func (s *Server) registerPageRoutes() {
	s.mux.Handle("GET /providers", s.adminOnlyMiddleware(http.HandlerFunc(s.pageProviders)))
	s.mux.HandleFunc("GET /agents", s.pageAgents)
	s.mux.HandleFunc("GET /channels", s.pageChannels)
	s.mux.Handle("GET /users", s.adminOnlyMiddleware(http.HandlerFunc(s.pageUsers)))
	s.mux.HandleFunc("GET /sessions", s.pageSessions)
	s.mux.HandleFunc("GET /sessions/{sessionID}", s.pageSessions)
	s.mux.HandleFunc("GET /scheduler", s.pageScheduler)
	s.mux.Handle("GET /plugins", s.adminOnlyMiddleware(http.HandlerFunc(s.pagePlugins)))
	s.mux.HandleFunc("GET /profile", s.pageProfile)
	s.mux.HandleFunc("GET /account", s.pageAccount)
	s.mux.HandleFunc("GET /credentials", s.pageCredentials)
}

func (s *Server) registerPluginRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/plugins", adminAPI(s.listPlugins))
	s.mux.Handle("GET /api/plugin-status/{kind}/{name}", adminAPI(s.getPluginStatus))
	s.mux.Handle("GET /api/plugin-config/{kind}/{name}", adminAPI(s.getPluginConfig))
	s.mux.Handle("GET /api/plugin-config-schema/{kind}/{name}", adminAPI(s.getPluginConfigSchema))
	s.mux.Handle("PATCH /api/plugins/{id...}", adminAPI(s.togglePlugin))
	s.mux.Handle("PUT /api/plugin-config/{kind}/{name}", adminAPI(s.updatePluginConfig))
}

func (s *Server) registerManifestPluginRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/manifest-plugins", adminAPI(s.listManifestPlugins))
	s.mux.Handle("PUT /api/manifest-plugins", adminAPI(s.saveManifestPlugins))
	s.mux.Handle("POST /api/manifest-plugins/sync", adminAPI(s.syncManifestPlugins))
}
