package admin

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"

	"filippo.io/age"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/oauthcli"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/vault"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
)

// Server provides HTTP handlers for the admin API and templ-rendered pages.
type Server struct {
	store          config.Store
	authStore      auth.AuthStore
	engine         *auth.PolicyEngine
	rateLimiter    *auth.RateLimiter
	linkCodes      *auth.LinkCodeStore
	mem            memory.Provider
	poolManager    *agent.PoolManager
	pluginHost     *pluginhost.Host
	db             *sql.DB
	q              *sqlc.Queries
	mux            *http.ServeMux
	log            *slog.Logger
	corsOriginV    string               // cached CORS origin
	vaultRecipient *age.X25519Recipient // optional; if set, age keys are generated for new users
	vaultSvc       *vault.Service       // optional; if nil, vault endpoints return 503

	// OAuth CLI device-flow state.
	flowStore        *oauthcli.FlowStore
	oauthMu          sync.Mutex
	ghBroker         *oauthcli.GitHubBroker // lazily initialised; guarded by oauthMu
	ghBrokerClientID string                 // tracks which client_id ghBroker was built with
	larkBroker       *oauthcli.LarkBroker   // lazily initialised; guarded by oauthMu
	larkBrokerAppID  string                 // tracks which app_id larkBroker was built with
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
		flowStore:   oauthcli.NewFlowStore(),
	}

	s.registerRoutes()

	return s
}

// LinkCodes returns the link code store for use by channel handlers.
func (s *Server) LinkCodes() *auth.LinkCodeStore {
	return s.linkCodes
}

// SetVaultRecipient sets the master age recipient so that new users created via
// web registration receive an age keypair. Call before serving requests.
// If not set (nil), vault key generation is skipped for new users.
func (s *Server) SetVaultRecipient(r *age.X25519Recipient) {
	s.vaultRecipient = r
}

// SetVaultService wires the vault service into the admin server.
// Call before serving requests. If not set (nil), vault API endpoints
// return 503 Service Unavailable.
func (s *Server) SetVaultService(svc *vault.Service) {
	s.vaultSvc = svc
}
