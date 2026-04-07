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
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/pluginhost"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
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

	// Serve static assets (JS modules).
	s.mux.Handle("GET /static/", staticHandler())

	// Login page (exempt from auth).
	s.mux.HandleFunc("GET /login", s.pageLogin)

	// Auth API routes (exempt from auth).
	s.mux.HandleFunc("POST /api/auth/register", s.registerHandler)
	s.mux.HandleFunc("POST /api/auth/login", s.loginHandler)
	s.mux.HandleFunc("POST /api/auth/logout", s.logoutHandler)
	s.mux.HandleFunc("GET /api/auth/me", s.meHandler)

	// Profile API routes (authenticated users).
	s.mux.HandleFunc("GET /api/auth/profile/identities", s.listProfileIdentities)
	s.mux.HandleFunc("PUT /api/auth/profile/password", s.changePassword)
	s.mux.HandleFunc("POST /api/auth/profile/link-code", s.generateLinkCode)
	s.mux.HandleFunc("DELETE /api/auth/profile/identities/{id}", s.unlinkIdentity)
	s.mux.HandleFunc("GET /api/auth/profile/memories", s.listProfileMemories)
	s.mux.HandleFunc("PUT /api/auth/profile/memories/{agentId}", s.setProfileMemory)
	s.mux.HandleFunc("DELETE /api/auth/profile/memories/{agentId}", s.deleteProfileMemory)
	s.mux.HandleFunc("PUT /api/auth/profile/soul/{agentId}", s.setProfileSoul)

	// Page routes — templ-rendered HTML pages.
	// Admin-only pages: providers, channels, users, plugins.
	s.mux.Handle("GET /providers", s.adminOnlyMiddleware(http.HandlerFunc(s.pageProviders)))
	s.mux.HandleFunc("GET /agents", s.pageAgents)
	s.mux.Handle("GET /channels", s.adminOnlyMiddleware(http.HandlerFunc(s.pageChannels)))
	s.mux.Handle("GET /users", s.adminOnlyMiddleware(http.HandlerFunc(s.pageUsers)))
	s.mux.HandleFunc("GET /sessions", s.pageSessions)
	s.mux.HandleFunc("GET /scheduler", s.pageScheduler)
	s.mux.Handle("GET /plugins", s.adminOnlyMiddleware(http.HandlerFunc(s.pagePlugins)))
	s.mux.HandleFunc("GET /profile", s.pageProfile)

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
	s.mux.Handle("GET /api/provider-types", adminAPI(s.listProviderTypes))

	// Agent APIs (read/create for all authenticated users, update/delete for admin or creator).
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/agents", s.createAgent)
	s.mux.HandleFunc("GET /api/agents/{id}", s.getAgent)
	s.mux.HandleFunc("PUT /api/agents/{id}", s.updateAgent)
	s.mux.HandleFunc("DELETE /api/agents/{id}", s.deleteAgent)

	// Agent user assignment APIs (admin-only).
	s.mux.Handle("GET /api/agents/{id}/users", adminAPI(s.listAgentUsers))
	s.mux.Handle("POST /api/agents/{id}/users", adminAPI(s.assignAgentUser))
	s.mux.Handle("DELETE /api/agents/{id}/users/{userId}", adminAPI(s.removeAgentUser))

	// Channel APIs (admin-only).
	s.mux.Handle("GET /api/channels", adminAPI(s.listChannels))
	s.mux.Handle("GET /api/channels/{platform}", adminAPI(s.getChannel))
	s.mux.Handle("PUT /api/channels/{platform}", adminAPI(s.updateChannel))

	// Weixin QR linking (authenticated — any user can link their account).
	s.mux.HandleFunc("POST /api/channels/weixin/qr", s.startWeixinQR)
	s.mux.HandleFunc("GET /api/channels/weixin/qr/status", s.pollWeixinQRStatus)

	// User APIs (admin-only) — memory management and default agent.
	s.mux.Handle("PUT /api/users/{id}/default-agent", adminAPI(s.updateUserDefaultAgent))
	s.mux.Handle("PUT /api/users/{id}/notify-identity", adminAPI(s.updateUserNotifyIdentity))
	s.mux.Handle("GET /api/users/{id}/memories", adminAPI(s.listUserMemories))
	s.mux.Handle("PUT /api/users/{id}/memories/{agentId}", adminAPI(s.setUserMemory))
	s.mux.Handle("DELETE /api/users/{id}/memories/{agentId}", adminAPI(s.deleteUserMemory))

	// Auth user management APIs (admin-only).
	s.mux.Handle("GET /api/auth/users", adminAPI(s.listAuthUsers))
	s.mux.Handle("GET /api/auth/users/{id}", adminAPI(s.getAuthUser))
	s.mux.Handle("PUT /api/auth/users/{id}/role", adminAPI(s.updateAuthUserRole))
	s.mux.Handle("GET /api/auth/users/{id}/agents", adminAPI(s.listAuthUserAgents))
	s.mux.Handle("PUT /api/auth/users/{id}/agents", adminAPI(s.updateAuthUserAgents))
	s.mux.Handle("DELETE /api/auth/users/{id}/identities/{identityId}", adminAPI(s.deleteAuthUserIdentity))
	s.mux.Handle("PUT /api/auth/users/{id}/active", adminAPI(s.updateAuthUserActive))

	// Session APIs.
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}", s.getSession)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/messages", s.getSessionMessages)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/system-prompt", s.getSessionSystemPrompt)

	// Plugin APIs (admin-only).
	s.mux.Handle("GET /api/plugins", adminAPI(s.listPlugins))
	s.mux.Handle("GET /api/plugin-status/{kind}/{name}", adminAPI(s.getPluginStatus))
	s.mux.Handle("GET /api/plugin-config-schema/{kind}/{name}", adminAPI(s.getPluginConfigSchema))
	s.mux.Handle("PATCH /api/plugins/{id...}", adminAPI(s.togglePlugin))
	s.mux.Handle("PUT /api/plugin-config/{kind}/{name}", adminAPI(s.updatePluginConfig))

	// Models API (cached models, no live provider calls).
	s.mux.HandleFunc("GET /api/models", s.listCachedModels)

	// Tools API (available tools for agents).
	s.mux.HandleFunc("GET /api/tools", s.listAgentTools)

	// Scheduler job APIs (all authenticated users; RBAC enforced in handlers).
	s.mux.HandleFunc("GET /api/scheduler/jobs", s.listSchedulerJobs)
	s.mux.HandleFunc("POST /api/scheduler/jobs", s.createSchedulerJob)
	s.mux.HandleFunc("PUT /api/scheduler/jobs/{id}", s.updateSchedulerJob)
	s.mux.HandleFunc("DELETE /api/scheduler/jobs/{id}", s.deleteSchedulerJob)

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
