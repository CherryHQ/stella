package admin

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/memory"
)

// Server provides HTTP handlers for the admin API and embedded SPA.
type Server struct {
	store config.Store
	mem   memory.Engine
	db    *sql.DB
	q     *sqlc.Queries
	mux   *http.ServeMux
	log   *slog.Logger
}

// New creates an admin server with all API routes mounted.
func New(store config.Store, mem memory.Engine, db *sql.DB) *Server {
	s := &Server{
		store: store,
		mem:   mem,
		db:    db,
		q:     sqlc.New(db),
		mux:   http.NewServeMux(),
		log:   slog.With("component", "admin"),
	}

	// Serve embedded SPA at root.
	s.mux.HandleFunc("GET /", s.serveUI)

	// Provider APIs.
	s.mux.HandleFunc("GET /api/providers", s.listProviders)
	s.mux.HandleFunc("POST /api/providers", s.createProvider)
	s.mux.HandleFunc("GET /api/providers/{id}", s.getProvider)
	s.mux.HandleFunc("PUT /api/providers/{id}", s.updateProvider)
	s.mux.HandleFunc("DELETE /api/providers/{id}", s.deleteProvider)
	s.mux.HandleFunc("POST /api/providers/{id}/models", s.fetchProviderModels)

	// Agent APIs.
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/agents", s.createAgent)
	s.mux.HandleFunc("GET /api/agents/{id}", s.getAgent)
	s.mux.HandleFunc("PUT /api/agents/{id}", s.updateAgent)
	s.mux.HandleFunc("DELETE /api/agents/{id}", s.deleteAgent)

	// Channel APIs.
	s.mux.HandleFunc("GET /api/channels", s.listChannels)
	s.mux.HandleFunc("GET /api/channels/{platform}", s.getChannel)
	s.mux.HandleFunc("PUT /api/channels/{platform}", s.updateChannel)

	// User APIs.
	s.mux.HandleFunc("GET /api/users", s.listUsers)

	// Session APIs.
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)

	// Settings APIs.
	s.mux.HandleFunc("GET /api/settings/{key}", s.getSetting)
	s.mux.HandleFunc("PUT /api/settings/{key}", s.updateSetting)

	// Scheduler job APIs.
	s.mux.HandleFunc("GET /api/scheduler/jobs", s.listSchedulerJobs)
	s.mux.HandleFunc("POST /api/scheduler/jobs", s.createSchedulerJob)
	s.mux.HandleFunc("PUT /api/scheduler/jobs/{id}", s.updateSchedulerJob)
	s.mux.HandleFunc("DELETE /api/scheduler/jobs/{id}", s.deleteSchedulerJob)

	return s
}

// Handler returns the HTTP handler with CORS and JSON middleware applied.
func (s *Server) Handler() http.Handler {
	return s.withMiddleware(s.mux)
}

// withMiddleware wraps the mux with CORS and JSON content-type headers.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS for local dev.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// JSON content-type for /api/ routes.
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
