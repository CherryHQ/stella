package server

import (
	"net/http"
	"strings"
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

// Handler returns the HTTP handler with CORS, JSON, and auth middleware applied.
func (s *Server) Handler() http.Handler {
	return s.corsMiddleware(s.authMiddleware(s.jsonMiddleware(s.mux)))
}

// corsMiddleware handles CORS headers. Origin is read from settings at startup.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.corsOriginV)
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
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" && !strings.HasPrefix(r.URL.Path, "/api/docs") {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}
