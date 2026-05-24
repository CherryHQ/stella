package server

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"filippo.io/age"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/localoidc"
	authoidc "github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/credentials"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Server provides HTTP handlers for the admin API and embedded web UI.
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
	tokenSvc       *auth.TokenService   // optional; if nil, bearer token auth is disabled
	credSvc        *credentials.Service // shared credentials service
	recally        *recallyHandlers     // recally HTTP API (articles, feeds, digest)
	schedulerSvc   *scheduler.Service   // optional; if set, create/delete go through the live scheduler
	tasksSvc       *tasks.Service       // optional; if nil, task endpoints return 503
	startedAt      time.Time
	// OIDC auth (optional; if nil, OIDC login is disabled)
	authProviders []auth.AuthProvider
	authSvc       *auth.AuthService
	sessionMgr    *auth.SessionManager
	stateMgr      *authoidc.StateManager
	// localOIDC is the built-in local OIDC issuer (optional).
	localOIDC *localoidc.Issuer
	// logins provides access to OIDC login identities (optional).
	logins auth.LoginIdentityStore
}

// New creates an admin server with all API routes mounted.
// The linkCodes store is shared with channel bots so codes generated in the
// Web UI can be consumed by channel handlers.
func New(store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, mem memory.Provider, db *sql.DB, linkCodes *auth.LinkCodeStore, poolManager *agent.PoolManager, pluginHost *pluginhost.Host) *Server {
	if pluginHost == nil {
		panic("admin: plugin host is required")
	}

	// Read CORS origin once at startup.
	corsOrigin := "http://localhost:8080"
	if val, err := store.GetSetting(context.Background(), "admin.cors_origin"); err == nil && val != "" {
		corsOrigin = val
	}

	flowStore := oauth.NewFlowStore()
	credSvc := credentials.NewService(nil, sqlc.New(db), flowStore, corsOrigin)

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
		credSvc:     credSvc,
		recally:     newRecallyHandlers(recally.NewStore(db), recally.NewFileManager(config.StellaHome())),
		startedAt:   time.Now(),
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
	s.credSvc.SetVaultService(svc)
}

// SetTokenService wires bearer token authentication into the admin server.
func (s *Server) SetTokenService(svc *auth.TokenService) {
	s.tokenSvc = svc
}

// SetSchedulerService wires the live scheduler service into the admin server.
// When set, create and delete job handlers go through the service (live + DB).
// If not set, those handlers write DB-only.
func (s *Server) SetSchedulerService(svc *scheduler.Service) {
	s.schedulerSvc = svc
}

// SetLocalOIDCIssuer wires the built-in local OIDC issuer into the server.
// When set, routes under /oidc/local/ are registered for the issuer endpoints.
// Call before serving requests.
func (s *Server) SetLocalOIDCIssuer(issuer *localoidc.Issuer) {
	s.localOIDC = issuer
	s.registerLocalOIDCRoutes()
}

// SetLoginIdentityStore wires the OIDC login identity store so the admin API
// can list and link login identities. Call before serving requests.
func (s *Server) SetLoginIdentityStore(store auth.LoginIdentityStore) {
	s.logins = store
}

// SetOIDCAuth wires all OIDC authentication components into the server.
// Call before serving requests. If not set, OIDC login is disabled and
// ListAuthProviders returns an empty list.
func (s *Server) SetOIDCAuth(
	providers []auth.AuthProvider,
	authSvc *auth.AuthService,
	sessionMgr *auth.SessionManager,
	stateMgr *authoidc.StateManager,
) {
	s.authProviders = providers
	s.authSvc = authSvc
	s.sessionMgr = sessionMgr
	s.stateMgr = stateMgr
}

// CredentialsService returns the shared credentials service.
// Used by callers that need to wire in the runner invalidator or access
// the credentials tool from outside the admin package.
func (s *Server) CredentialsService() *credentials.Service {
	return s.credSvc
}

// Recally method delegations — Server implements apiserver.ServerInterface by
// forwarding all recally operations to the embedded recallyHandlers.

func (s *Server) ListArticles(w http.ResponseWriter, r *http.Request, params apiserver.ListArticlesParams) {
	s.recally.ListArticles(w, r, params)
}

func (s *Server) SaveArticle(w http.ResponseWriter, r *http.Request) {
	s.recally.SaveArticle(w, r)
}

func (s *Server) DeleteArticle(w http.ResponseWriter, r *http.Request, id string) {
	s.recally.DeleteArticle(w, r, id)
}

func (s *Server) GetArticle(w http.ResponseWriter, r *http.Request, id string, params apiserver.GetArticleParams) {
	s.recally.GetArticle(w, r, id, params)
}

func (s *Server) UpdateArticle(w http.ResponseWriter, r *http.Request, id string) {
	s.recally.UpdateArticle(w, r, id)
}

func (s *Server) GetDigest(w http.ResponseWriter, r *http.Request) {
	s.recally.GetDigest(w, r)
}

func (s *Server) ListStoredDigests(w http.ResponseWriter, r *http.Request, params apiserver.ListStoredDigestsParams) {
	s.recally.ListStoredDigests(w, r, params)
}

func (s *Server) SaveDigest(w http.ResponseWriter, r *http.Request) {
	s.recally.SaveDigest(w, r)
}

func (s *Server) GetStoredDigest(w http.ResponseWriter, r *http.Request, date string) {
	s.recally.GetStoredDigest(w, r, date)
}

func (s *Server) ListFeeds(w http.ResponseWriter, r *http.Request, params apiserver.ListFeedsParams) {
	s.recally.ListFeeds(w, r, params)
}

func (s *Server) CreateFeed(w http.ResponseWriter, r *http.Request) {
	s.recally.CreateFeed(w, r)
}

func (s *Server) ListFeedEntries(w http.ResponseWriter, r *http.Request, feedId string, params apiserver.ListFeedEntriesParams) {
	s.recally.ListFeedEntries(w, r, feedId, params)
}

func (s *Server) UpdateFeedEntry(w http.ResponseWriter, r *http.Request, feedId string, id string) {
	s.recally.UpdateFeedEntry(w, r, feedId, id)
}

func (s *Server) DeleteFeed(w http.ResponseWriter, r *http.Request, id string) {
	s.recally.DeleteFeed(w, r, id)
}

func (s *Server) GetFeed(w http.ResponseWriter, r *http.Request, id string) {
	s.recally.GetFeed(w, r, id)
}

func (s *Server) UpdateFeed(w http.ResponseWriter, r *http.Request, id string) {
	s.recally.UpdateFeed(w, r, id)
}

func (s *Server) PollFeed(w http.ResponseWriter, r *http.Request, id string, params apiserver.PollFeedParams) {
	s.recally.PollFeed(w, r, id, params)
}
