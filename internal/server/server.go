package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/jackc/pgx/v5/pgxpool"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	authoidc "github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/internal/oidc"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/vault"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Server provides HTTP handlers for the admin API and embedded web UI.
type Server struct {
	store            config.Store
	authStore        auth.AuthStore
	agentAccess      *agentaccess.Service
	sessionAccess    *sessionaccess.Service
	skillAccess      *skillaccess.Service
	rateLimiter      *auth.RateLimiter
	linkCodes        *auth.LinkCodeStore
	mem              memory.Provider
	memoryManagement *memorywrite.ManagementService
	poolManager      *agent.PoolManager
	pluginHost       *pluginhost.Host
	weixinRegistrar  WeixinRegistrar
	db               *pgxpool.Pool
	q                *sqlc.Queries
	mux              *http.ServeMux
	log              *slog.Logger
	vaultRecipient   *age.X25519Recipient  // optional; if set, age keys are generated for new users
	vaultSvc         *vault.Service        // optional; if nil, vault endpoints return 503
	mcpSvc           *mcp.Service          // optional; if nil, MCP endpoints return 503
	credResolver     *credential.Service   // unified bearer credential front door
	oauthAS          *oidc.Service         // OAuth2 authorization server
	controlPlane     *controlplane.Service // control-plane PEP (providers/settings/plugins/channels)
	credSvc          *connections.Service  // shared credentials service
	emailSvc         *email.Service        // shared email service
	shareSvc         *sharepkg.Service     // shared share service
	recallySvc       *recally.Service      // shared recally service
	recally          *recallyHandlers      // recally HTTP API (articles, feeds, digest)
	schedulerSvc     *scheduler.Service    // optional; if set, create/delete go through the live scheduler
	goalSvc          *goal.Service         // optional; if nil, goal endpoints return 503
	workflowSvc      *workflowpkg.Service  // optional; if nil, workflow endpoints return 503
	builtinTools     []agent.BuiltinTool
	startedAt        time.Time
	// OIDC auth (optional; if nil, OIDC login is disabled)
	authProviders []auth.AuthProvider
	authSvc       *auth.AuthService
	sessionMgr    *auth.SessionManager
	stateMgr      *authoidc.StateManager
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
	// groupDispatcher runs the shared durable group dispatch flow for Web sends.
	groupDispatcher *channel.GroupDispatcher
	// assets is the authoritative asset persistence service backing the session
	// workspace file handlers (durable-write/restore/move/delete for user assets).
	assets *asset.Store
	// runtimeCtx is canceled by the process/service lifecycle; request handlers
	// derive long-running work from it instead of client connections.
	runtimeCtx context.Context
	// webhookLimiter throttles accepted webhook ingress calls per instance.
	webhookLimiter *webhookLimiter
	// readiness backs the /healthz and /readyz infrastructure probes and carries
	// the graceful-drain signal streaming handlers watch.
	readiness *readiness
}

// Deps is the immutable, validated dependency set for the admin Server. The
// composition root constructs every value exactly once — including the single
// shared credentials/email/share/recally instances and the credential front
// door, already resolved against the final base URL — and hands them here.
// Server.New reads no environment, constructs no shadow service, and chooses no
// implementation; it only wires these into HTTP routes. The struct is copied
// into the Server at construction and never mutated afterward: there are no
// post-construction setters.
type Deps struct {
	// Persistence reached directly by handlers not yet migrated onto narrow
	// ports. This is the frozen per-file persistence debt tracked by the
	// architecture boundary test — carried explicitly here rather than hidden
	// behind a facade, and shrunk as later stacks migrate each handler onto a
	// domain service.
	Store            config.Store
	DB               *pgxpool.Pool
	AuthStore        auth.AuthStore
	Mem              memory.Provider
	MemoryManagement *memorywrite.ManagementService

	// Authorization. AgentAccess is the agent policy-enforcement point (the unified
	// Authorizer behind a deep agent service); there is no legacy PolicyEngine here.
	AgentAccess   *agentaccess.Service
	SessionAccess *sessionaccess.Service
	// SkillAccess is the DB-backed Skill policy-enforcement point. When nil the
	// skill endpoints report 503 through the centralized unavailable mapping.
	SkillAccess *skillaccess.Service
	LinkCodes   *auth.LinkCodeStore
	OIDC        OIDCDeps

	// Agent runtime + plugins.
	PoolManager  *agent.PoolManager
	PluginHost   *pluginhost.Host
	BuiltinTools []agent.BuiltinTool

	// WeixinRegistrar reaches the iLink API for the WeChat QR/registration
	// handlers. The composition root supplies the concrete adapter (which wraps
	// the weixin plugin client) so internal/server never imports the plugin.
	WeixinRegistrar WeixinRegistrar

	// Public addressing, resolved once at the startup boundary. Never a
	// localhost placeholder mutated later.
	BaseURL string

	// Shared domain services — single, fully-wired instances built by the
	// composition root. The same instances back both the agent tools and the
	// HTTP endpoints, so there is one source of truth per capability.
	Credentials         *connections.Service
	ControlPlane        *controlplane.Service
	Email               *email.Service
	Share               *sharepkg.Service
	Recally             *recally.Service
	CredentialFrontDoor *credential.Service
	OAuthAuthServer     *oidc.Service
	EventLog            *eventlog.Store
	GroupDispatcher     *channel.GroupDispatcher
	// Assets is the authoritative asset persistence service. Session workspace
	// handlers use its narrow durable-write/restore/move/delete ports instead of a
	// raw blob store, so the HTTP transport never holds process-global blob state.
	Assets *asset.Store

	// Optional capabilities. A nil field is a supported configuration: the
	// matching endpoints report 503 through the centralized unavailable mapping
	// (see capabilityUnavailable). Presence is never inferred from the
	// environment inside the server.
	Vault          *vault.Service
	VaultRecipient *age.X25519Recipient
	MCP            *mcp.Service
	Scheduler      *scheduler.Service
	Goal           *goal.Service
	Workflow       *workflowpkg.Service
}

// OIDCDeps groups the login-authentication components produced by oidc.Setup
// plus the shared identity stores the auth handlers read. LocalAuth is nil in
// external-OIDC mode; the identity stores are always present.
type OIDCDeps struct {
	Providers  []auth.AuthProvider
	AuthSvc    *auth.AuthService
	SessionMgr *auth.SessionManager
	StateMgr   *authoidc.StateManager
	LocalAuth  *local.Service

	Logins auth.LoginIdentityStore
	Users  interface {
		auth.UserStore
		auth.ChannelIdentityStore
	}
	Sessions    auth.SessionStore
	Credentials auth.CredentialStore
}

// validate reports every missing required dependency at once (fail-fast). A
// dependency is required only when the server dereferences it unconditionally
// (no 503 gate). Optional capabilities — CredentialFrontDoor, OAuthAuthServer,
// EventLog, GroupDispatcher, Vault, VaultRecipient, MCP, Scheduler,
// Goal, Workflow, and OIDC.StateMgr/LocalAuth — are intentionally not checked:
// a nil there is a supported configuration whose endpoints degrade to 503.
func (d Deps) validate() error {
	var missing []string
	req := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	req(d.Store != nil, "Store")
	req(d.DB != nil, "DB")
	req(d.AuthStore != nil, "AuthStore")
	req(d.Mem != nil, "Mem")
	req(d.MemoryManagement != nil, "MemoryManagement")
	req(d.AgentAccess != nil, "AgentAccess")
	req(d.SessionAccess != nil, "SessionAccess")
	req(d.LinkCodes != nil, "LinkCodes")
	req(d.PoolManager != nil, "PoolManager")
	req(d.PluginHost != nil, "PluginHost")
	req(d.WeixinRegistrar != nil, "WeixinRegistrar")
	req(d.BaseURL != "", "BaseURL")
	req(d.Credentials != nil, "Credentials")
	req(d.ControlPlane != nil, "ControlPlane")
	req(d.Email != nil, "Email")
	req(d.Share != nil, "Share")
	req(d.Recally != nil, "Recally")
	req(d.Assets != nil, "Assets")
	req(d.OIDC.AuthSvc != nil, "OIDC.AuthSvc")
	req(d.OIDC.SessionMgr != nil, "OIDC.SessionMgr")
	req(d.OIDC.Logins != nil, "OIDC.Logins")
	req(d.OIDC.Users != nil, "OIDC.Users")
	req(d.OIDC.Sessions != nil, "OIDC.Sessions")
	req(d.OIDC.Credentials != nil, "OIDC.Credentials")
	if len(missing) > 0 {
		return fmt.Errorf("server.New: missing required dependencies: %s", strings.Join(missing, ", "))
	}
	return nil
}

// New creates an admin server with all API routes mounted. It validates deps
// and returns an error if any required dependency is missing. It reads no
// environment, constructs no shared/shadow service, and installs no setters —
// every dependency arrives through the immutable Deps. The server does not own
// the lifecycle of any injected dependency and never closes one.
func New(ctx context.Context, deps Deps) (*Server, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := deps.validate(); err != nil {
		return nil, err
	}

	log := slog.With("component", "admin")
	s := &Server{
		store:            deps.Store,
		authStore:        deps.AuthStore,
		agentAccess:      deps.AgentAccess,
		sessionAccess:    deps.SessionAccess,
		skillAccess:      deps.SkillAccess,
		rateLimiter:      auth.NewRateLimiter(),
		webhookLimiter:   newWebhookLimiter(5, 20),
		linkCodes:        deps.LinkCodes,
		mem:              deps.Mem,
		memoryManagement: deps.MemoryManagement,
		poolManager:      deps.PoolManager,
		db:               deps.DB,
		pluginHost:       deps.PluginHost,
		weixinRegistrar:  deps.WeixinRegistrar,
		q:                sqlc.New(deps.DB),
		mux:              http.NewServeMux(),
		log:              log,
		baseURL:          deps.BaseURL,
		builtinTools:     append([]agent.BuiltinTool(nil), deps.BuiltinTools...),
		vaultRecipient:   deps.VaultRecipient,
		vaultSvc:         deps.Vault,
		mcpSvc:           deps.MCP,
		credResolver:     deps.CredentialFrontDoor,
		oauthAS:          deps.OAuthAuthServer,
		controlPlane:     deps.ControlPlane,
		credSvc:          deps.Credentials,
		emailSvc:         deps.Email,
		shareSvc:         deps.Share,
		recallySvc:       deps.Recally,
		recally:          newRecallyHandlersWithService(deps.Recally, log),
		schedulerSvc:     deps.Scheduler,
		goalSvc:          deps.Goal,
		workflowSvc:      deps.Workflow,
		eventLog:         deps.EventLog,
		groupDispatcher:  deps.GroupDispatcher,
		assets:           deps.Assets,
		authProviders:    deps.OIDC.Providers,
		authSvc:          deps.OIDC.AuthSvc,
		sessionMgr:       deps.OIDC.SessionMgr,
		stateMgr:         deps.OIDC.StateMgr,
		localAuth:        deps.OIDC.LocalAuth,
		logins:           deps.OIDC.Logins,
		users:            deps.OIDC.Users,
		sessions:         deps.OIDC.Sessions,
		credentials:      deps.OIDC.Credentials,
		startedAt:        time.Now(),
		runtimeCtx:       ctx,
	}
	// Drain signal is a child of runtimeCtx so a hard process stop also releases
	// streaming handlers. The pool answers the /readyz liveness ping.
	s.readiness = newReadiness(ctx, deps.DB)

	s.registerRoutes()

	return s, nil
}

// NewCredentialFrontDoor builds the unified bearer credential front door (PAT +
// OAuth access storage over the shared queries) and the OAuth2 authorization
// server that mints access tokens through it. The composition root calls this
// once and injects both into server.Deps; Server.New never constructs them, so
// the credential surface has a single owner. The authorization server mints
// access tokens through the front door (never its own JWT) and owns the
// client/code/refresh storage.
func NewCredentialFrontDoor(db *pgxpool.Pool, log *slog.Logger) (*credential.Service, *oidc.Service) {
	q := sqlc.New(db)
	ps := patStore{q: q}
	os := oauthStore{q: q}
	resolver := credential.NewService(credential.Config{
		PATs:   ps,
		OAuth:  os,
		Users:  ps,
		Logger: log,
	})
	authServer := oidc.NewService(oidc.Config{
		Store:  os,
		Issuer: resolver,
		Logger: log,
	})
	return resolver, authServer
}

// MarkStartupComplete makes /readyz eligible to report ready. The gateway calls
// it once every subsystem has started, just before it blocks on shutdown.
func (s *Server) MarkStartupComplete() { s.readiness.markStartupComplete() }

// BeginDrain starts graceful shutdown from the server's side: /readyz flips to
// 503 and streaming handlers unwind. The shutdown orchestrator calls it before
// it touches the HTTP listener.
func (s *Server) BeginDrain() { s.readiness.beginDrain() }

// LinkCodes returns the link code store for use by channel handlers.
func (s *Server) LinkCodes() *auth.LinkCodeStore {
	return s.linkCodes
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
