package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginhost"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
)

// Server provides HTTP handlers for the admin API and templ-rendered pages.
type Server struct {
	store       config.Store
	authStore   auth.AuthStore
	engine      *auth.PolicyEngine
	rateLimiter *auth.RateLimiter
	linkCodes   *auth.LinkCodeStore
	mem         memory.Provider
	poolManager *agent.PoolManager
	pluginHost  *pluginhost.Host
	db          *sql.DB
	q           *sqlc.Queries
	mux         *http.ServeMux
	log         *slog.Logger
	corsOriginV string // cached CORS origin

	channelMu       sync.RWMutex
	channelStop     map[string]func()                             // platform → stop function for running channel
	channelBuilder  func(name string) (pkgchannel.Channel, error) // builds a channel by platform name
	channelNotify   func(name string, ch pkgchannel.Channel)      // registers channel for notifications
	channelUnnotify func(name string)                             // unregisters channel from notifications
	channelCtx      context.Context                               // parent context for started channels
}

// New creates an admin server with all API routes mounted.
// The linkCodes store is shared with channel bots so codes generated in the
// admin panel can be consumed by channel handlers.
func New(store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, mem memory.Provider, db *sql.DB, linkCodes *auth.LinkCodeStore, poolManager *agent.PoolManager, pluginHost *pluginhost.Host) *Server {
	if pluginHost == nil {
		panic("admin: plugin host is required")
	}

	// Read CORS origin once at startup.
	corsOrigin := "http://localhost:8080"
	if val, err := store.GetSetting(context.Background(), "admin.cors_origin"); err == nil && val != "" {
		corsOrigin = val
	}

	s := &Server{
		store:       store,
		authStore:   authStore,
		engine:      engine,
		rateLimiter: auth.NewRateLimiter(),
		linkCodes:   linkCodes,
		mem:         mem,
		poolManager: poolManager,
		db:          db,
		pluginHost:  pluginHost,
		q:           sqlc.New(db),
		mux:         http.NewServeMux(),
		log:         slog.With("component", "admin"),
		corsOriginV: corsOrigin,
		channelStop: make(map[string]func()),
	}

	s.registerRoutes()

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

// LinkCodes returns the link code store for use by channel handlers.
func (s *Server) LinkCodes() *auth.LinkCodeStore {
	return s.linkCodes
}

// RegisterChannelStop registers a stop function for a running channel so the
// admin panel can stop it when it is disabled via the UI.
func (s *Server) RegisterChannelStop(platform string, stop func()) {
	s.channelMu.Lock()
	s.channelStop[platform] = stop
	s.channelMu.Unlock()
}

// SetChannelLifecycle configures the admin server to start/stop channels dynamically.
// builder creates a channel by platform name; notify registers it for notifications;
// unnotify removes it from the notification dispatcher on stop.
func (s *Server) SetChannelLifecycle(
	ctx context.Context,
	builder func(name string) (pkgchannel.Channel, error),
	notify func(name string, ch pkgchannel.Channel),
	unnotify func(name string),
) {
	s.channelMu.Lock()
	s.channelBuilder = builder
	s.channelNotify = notify
	s.channelUnnotify = unnotify
	s.channelCtx = ctx
	s.channelMu.Unlock()
}

// startChannel builds, starts, and registers a channel for the given platform.
// No-op if the channel is already running or no builder is configured.
func (s *Server) startChannel(platform string) {
	s.channelMu.RLock()
	_, running := s.channelStop[platform]
	builder := s.channelBuilder
	notify := s.channelNotify
	ctx := s.channelCtx
	s.channelMu.RUnlock()

	if running || builder == nil {
		return
	}

	ch, err := builder(platform)
	if err != nil {
		s.log.Error("failed to build channel", "platform", platform, "error", err)
		return
	}

	s.RegisterChannelStop(platform, ch.Stop)
	if notify != nil {
		notify(platform, ch)
	}

	s.log.Info("starting channel", "platform", platform)
	go func() {
		if err := ch.Start(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("channel stopped with error", "platform", platform, "error", err)
		}
	}()
}

// stopChannel stops a running channel if one is registered for the platform.
func (s *Server) stopChannel(platform string) {
	s.channelMu.RLock()
	stop, ok := s.channelStop[platform]
	unnotify := s.channelUnnotify
	s.channelMu.RUnlock()
	if ok {
		s.log.Info("stopping channel", "platform", platform)
		stop()
		if unnotify != nil {
			unnotify(platform)
		}
		s.channelMu.Lock()
		delete(s.channelStop, platform)
		s.channelMu.Unlock()
	}
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
