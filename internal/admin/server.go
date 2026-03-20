package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/memory"
)

// Server provides HTTP handlers for the admin API and templ-rendered pages.
type Server struct {
	store       config.Store
	authStore   auth.AuthStore
	engine      *auth.PolicyEngine
	rateLimiter *auth.RateLimiter
	mem         memory.Engine
	db          *sql.DB
	q           *sqlc.Queries
	mux         *http.ServeMux
	log         *slog.Logger
}

// New creates an admin server with all API routes mounted.
func New(store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, mem memory.Engine, db *sql.DB) *Server {
	s := &Server{
		store:       store,
		authStore:   authStore,
		engine:      engine,
		rateLimiter: auth.NewRateLimiter(),
		mem:         mem,
		db:          db,
		q:           sqlc.New(db),
		mux:         http.NewServeMux(),
		log:         slog.With("component", "admin"),
	}

	// Serve static assets (JS modules).
	s.mux.Handle("GET /static/", staticHandler())

	// Login page (exempt from auth).
	s.mux.HandleFunc("GET /login", s.pageLogin)

	// Auth API routes (exempt from auth).
	s.mux.HandleFunc("POST /api/auth/register", s.registerHandler)
	s.mux.HandleFunc("POST /api/auth/login", s.loginHandler)
	s.mux.HandleFunc("POST /api/auth/logout", s.logoutHandler)
	s.mux.HandleFunc("GET /api/auth/me", s.meHandler)

	// Page routes — templ-rendered HTML pages.
	// Admin-only pages: providers, channels, users, scheduler, settings.
	s.mux.Handle("GET /providers", s.adminOnlyMiddleware(http.HandlerFunc(s.pageProviders)))
	s.mux.HandleFunc("GET /agents", s.pageAgents)
	s.mux.Handle("GET /channels", s.adminOnlyMiddleware(http.HandlerFunc(s.pageChannels)))
	s.mux.Handle("GET /users", s.adminOnlyMiddleware(http.HandlerFunc(s.pageUsers)))
	s.mux.HandleFunc("GET /sessions", s.pageSessions)
	s.mux.Handle("GET /scheduler", s.adminOnlyMiddleware(http.HandlerFunc(s.pageScheduler)))
	s.mux.Handle("GET /settings", s.adminOnlyMiddleware(http.HandlerFunc(s.pageSettings)))

	// Root redirect based on auth status.
	s.mux.HandleFunc("GET /{$}", s.redirectRoot)

	// Admin-only API routes.
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}

	// Provider APIs (admin-only).
	s.mux.Handle("GET /api/providers", adminAPI(s.listProviders))
	s.mux.Handle("POST /api/providers", adminAPI(s.createProvider))
	s.mux.Handle("GET /api/providers/{id}", adminAPI(s.getProvider))
	s.mux.Handle("PUT /api/providers/{id}", adminAPI(s.updateProvider))
	s.mux.Handle("DELETE /api/providers/{id}", adminAPI(s.deleteProvider))
	s.mux.Handle("POST /api/providers/{id}/models", adminAPI(s.fetchProviderModels))

	// Agent APIs.
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/agents", s.createAgent)
	s.mux.HandleFunc("GET /api/agents/{id}", s.getAgent)
	s.mux.HandleFunc("PUT /api/agents/{id}", s.updateAgent)
	s.mux.HandleFunc("DELETE /api/agents/{id}", s.deleteAgent)

	// Channel APIs (admin-only).
	s.mux.Handle("GET /api/channels", adminAPI(s.listChannels))
	s.mux.Handle("GET /api/channels/{platform}", adminAPI(s.getChannel))
	s.mux.Handle("PUT /api/channels/{platform}", adminAPI(s.updateChannel))

	// User APIs (admin-only).
	s.mux.Handle("GET /api/users", adminAPI(s.listUsers))
	s.mux.Handle("PUT /api/users/{id}", adminAPI(s.updateUser))
	s.mux.Handle("GET /api/users/{id}/memories", adminAPI(s.listUserMemories))
	s.mux.Handle("PUT /api/users/{id}/memories/{agentId}", adminAPI(s.setUserMemory))
	s.mux.Handle("DELETE /api/users/{id}/memories/{agentId}", adminAPI(s.deleteUserMemory))

	// Session APIs.
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}", s.getSession)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/messages", s.getSessionMessages)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/system-prompt", s.getSessionSystemPrompt)

	// Settings APIs (admin-only).
	s.mux.Handle("GET /api/settings/{key}", adminAPI(s.getSetting))
	s.mux.Handle("PUT /api/settings/{key}", adminAPI(s.updateSetting))

	// Models API (cached models, no live provider calls).
	s.mux.HandleFunc("GET /api/models", s.listCachedModels)

	// Tools API (available tools for agents).
	s.mux.HandleFunc("GET /api/tools", s.listAgentTools)

	// Scheduler job APIs (admin-only).
	s.mux.Handle("GET /api/scheduler/jobs", adminAPI(s.listSchedulerJobs))
	s.mux.Handle("POST /api/scheduler/jobs", adminAPI(s.createSchedulerJob))
	s.mux.Handle("PUT /api/scheduler/jobs/{id}", adminAPI(s.updateSchedulerJob))
	s.mux.Handle("DELETE /api/scheduler/jobs/{id}", adminAPI(s.deleteSchedulerJob))

	return s
}

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

// corsMiddleware handles CORS headers. Origin is configurable via the
// settings table key "admin.cors_origin"; defaults to http://localhost:8080.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.corsOrigin()
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// corsOrigin reads the CORS origin from the settings table. Falls back to
// http://localhost:8080 if not configured.
func (s *Server) corsOrigin() string {
	val, err := s.store.GetSetting(context.Background(), "admin.cors_origin")
	if err == nil && val != "" {
		return val
	}
	return "http://localhost:8080"
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

// writeData writes a success JSON response with the given data.
func writeData(w http.ResponseWriter, status int, data any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// writeError writes an error JSON response.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// decodeJSON reads a JSON body into dst.
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
