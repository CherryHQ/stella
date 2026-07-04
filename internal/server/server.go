package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"filippo.io/age"

	"github.com/jackc/pgx/v5/pgxpool"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/credentials"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/memory"
	oauthas "github.com/CherryHQ/stella/internal/oauth"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/vault"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
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
	db             *pgxpool.Pool
	q              *sqlc.Queries
	mux            *http.ServeMux
	log            *slog.Logger
	vaultRecipient *age.X25519Recipient // optional; if set, age keys are generated for new users
	vaultSvc       *vault.Service       // optional; if nil, vault endpoints return 503
	mcpSvc         *mcp.Service         // optional; if nil, MCP endpoints return 503
	tokenSvc       *auth.TokenService   // optional; if nil, bearer token auth is disabled
	credResolver   *credential.Service  // unified bearer credential front door (set with tokenSvc)
	oauthAS        *oauthas.Service     // OAuth2 authorization server (set with tokenSvc)
	credSvc        *credentials.Service // shared credentials service
	recally        *recallyHandlers     // recally HTTP API (articles, feeds, digest)
	schedulerSvc   *scheduler.Service   // optional; if set, create/delete go through the live scheduler
	goalSvc        *goal.GoalService    // optional; if nil, goal endpoints return 503
	goalQueries    *sqlc.Queries        // optional; read side for goal endpoints
	workflowSvc    *workflowpkg.Service // optional; if nil, workflow endpoints return 503
	startedAt      time.Time
	// OIDC auth (optional; if nil, OIDC login is disabled)
	authProviders []auth.AuthProvider
	authSvc       *auth.AuthService
	sessionMgr    *auth.SessionManager
	stateMgr      *oidc.StateManager
	localAuth     *local.Service
	// baseURL is the public URL for this instance (from STELLA_BASE_URL).
	baseURL string
	// logins provides access to OIDC login identities (optional).
	logins auth.LoginIdentityStore
	// users provides access to auth_user and channel_identity via the OIDC store (optional).
	users interface {
		auth.UserStore
		auth.ChannelIdentityStore
	}
	// sessions provides access to auth_session (optional).
	sessions auth.SessionStore
	// credentials provides access to auth_credential (optional).
	credentials auth.CredentialStore
	// eventLog is the group event log store (optional; if nil, group chat returns 503).
	eventLog *eventlog.Store
	// arbiter decides which agents respond in Web group chat (optional; if nil, group chat returns 503).
	arbiter *channel.Arbiter
	// groupDispatcher runs the shared durable group dispatch flow for Web sends.
	groupDispatcher *channel.GroupDispatcher
	// runtimeCtx is canceled by the process/service lifecycle; request handlers
	// derive long-running work from it instead of client connections.
	runtimeCtx context.Context
}

// New creates an admin server with all API routes mounted.
// The linkCodes store is shared with channel bots so codes generated in the
// Web UI can be consumed by channel handlers.
func New(ctx context.Context, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, mem memory.Provider, db *pgxpool.Pool, linkCodes *auth.LinkCodeStore, poolManager *agent.PoolManager, pluginHost *pluginhost.Host) *Server {
	if ctx == nil {
		ctx = context.Background()
	}
	if pluginHost == nil {
		panic("admin: plugin host is required")
	}

	defaultBaseURL := "http://localhost:25678"
	flowStore := oauth.NewFlowStore()
	credSvc := credentials.NewService(nil, sqlc.New(db), flowStore, defaultBaseURL)

	log := slog.With("component", "admin")
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
		log:         log,
		baseURL:     defaultBaseURL,
		credSvc:     credSvc,
		recally:     newRecallyHandlers(recally.NewStore(db), recally.NewFileManager(config.StellaHome()), log),
		startedAt:   time.Now(),
		runtimeCtx:  ctx,
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

// SetMCPService wires the MCP registration service into the admin server.
// Call before serving requests. If not set (nil), MCP API endpoints return 503.
func (s *Server) SetMCPService(svc *mcp.Service) {
	s.mcpSvc = svc
}

// SetTokenService wires bearer token authentication into the admin server. It
// also builds the unified credential front door: PAT storage over the shared
// query set, and legacy/scoped bearer verification delegated to the token
// service.
func (s *Server) SetTokenService(svc *auth.TokenService) {
	s.tokenSvc = svc
	ps := patStore{q: s.q}
	os := oauthStore{q: s.q}
	s.credResolver = credential.NewService(credential.Config{
		PATs:   ps,
		OAuth:  os,
		Users:  ps,
		Tokens: tokenBackend{svc: svc},
		Logger: s.log,
	})
	// The authorization server mints access tokens through the credential front
	// door (never its own JWT) and owns the client/code/refresh storage.
	s.oauthAS = oauthas.NewService(oauthas.Config{
		Store:  os,
		Issuer: s.credResolver,
		Logger: s.log,
	})
}

// SetSchedulerService wires the live scheduler service into the admin server.
// When set, create and delete job handlers go through the service (live + DB).
// If not set, those handlers write DB-only.
func (s *Server) SetSchedulerService(svc *scheduler.Service) {
	s.schedulerSvc = svc
}

func (s *Server) SetEventLogStore(store *eventlog.Store) {
	s.eventLog = store
}

func (s *Server) SetArbiter(a *channel.Arbiter) {
	s.arbiter = a
}

func (s *Server) SetGroupDispatcher(dispatcher *channel.GroupDispatcher) {
	s.groupDispatcher = dispatcher
}

// SetBaseURL sets the public base URL and propagates it to the credentials
// service so OAuth redirect URIs use the externally reachable address.
func (s *Server) SetBaseURL(url string) {
	s.baseURL = url
	s.credSvc.SetBaseURL(url)
}

// SetLoginIdentityStore wires the OIDC login identity store so the admin API
// can list and link login identities. Call before serving requests.
func (s *Server) SetLoginIdentityStore(store auth.LoginIdentityStore) {
	s.logins = store
}

// SetUserStore wires the OIDC user+identity store into the admin server.
func (s *Server) SetUserStore(store interface {
	auth.UserStore
	auth.ChannelIdentityStore
},
) {
	s.users = store
}

// SetSessionStore wires the session store into the admin server.
func (s *Server) SetSessionStore(store auth.SessionStore) {
	s.sessions = store
}

// SetCredentialStore wires the credential store into the admin server.
func (s *Server) SetCredentialStore(store auth.CredentialStore) {
	s.credentials = store
}

// SetOIDCAuth wires all OIDC authentication components into the server.
// Call before serving requests. If not set, OIDC login is disabled and
// ListAuthProviders returns an empty list.
func (s *Server) SetOIDCAuth(result *oidc.SetupResult) {
	s.authProviders = result.Providers
	s.authSvc = result.AuthSvc
	s.sessionMgr = result.SessionMgr
	s.stateMgr = result.StateMgr
	s.localAuth = result.LocalAuth
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

func (s *Server) CreateFeedEntry(w http.ResponseWriter, r *http.Request, feedId string) {
	s.recally.CreateFeedEntry(w, r, feedId)
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
