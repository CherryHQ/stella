package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"filippo.io/age"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/account"
	authoidc "github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/inbox"
	"github.com/CherryHQ/stella/internal/mcp"
	memprofile "github.com/CherryHQ/stella/internal/memory/profile"
	"github.com/CherryHQ/stella/internal/oidc"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/internal/webhook"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
)

// Server provides HTTP handlers for the admin API and embedded web UI.
type Server struct {
	channelResolver *channel.RuntimeResolver
	account         *account.Service
	profileSvc      *memprofile.Service
	projectStore    *agent.ProjectStore
	inboxSvc        *inbox.Service
	agentAccess     *agentaccess.Service
	agentManagement *agentaccess.Management
	toolOverrides   *agent.ToolOverrideStore
	sessionAccess   *sessionaccess.Service
	skillAccess     *skillaccess.Service
	rateLimiter     *auth.RateLimiter
	linkCodes       *auth.LinkCodeStore
	poolManager     *agent.PoolManager
	pluginHost      *pluginhost.Host
	weixinRegistrar WeixinRegistrar
	// pinger is the narrow database-liveness port backing the /healthz, /readyz,
	// and admin status probes. It is the injected pool viewed as DBPinger, so the
	// probes can never reach an application query.
	pinger         DBPinger
	mux            *http.ServeMux
	log            *slog.Logger
	vaultRecipient *age.X25519Recipient  // optional; if set, age keys are generated for new users
	vaultSvc       *vault.Service        // optional; if nil, vault endpoints return 503
	mcpSvc         *mcp.Service          // optional; if nil, MCP endpoints return 503
	credResolver   *credential.Service   // unified bearer credential front door
	oauthAS        *oidc.Service         // OAuth2 authorization server
	controlPlane   *controlplane.Service // control-plane PEP (providers/settings/plugins/channels)
	credSvc        *connections.Service  // shared credentials service
	emailSvc       *email.Service        // shared email service
	shareSvc       *sharepkg.Service     // shared share service
	recallySvc     *recally.Service      // shared recally service
	recally        *recallyHandlers      // recally HTTP API (articles, feeds, digest)
	schedulerSvc   *scheduler.Service    // optional; if set, create/delete go through the live scheduler
	goalSvc        *goal.Service         // optional; if nil, goal endpoints return 503
	workflowSvc    *workflowpkg.Service  // optional; if nil, workflow endpoints return 503
	webhookIngress webhookIngressPort    // narrow capability-domain adapter
	webhookRun     webhookRunPort        // ingress-only agent execution adapter
	builtinTools   []agent.BuiltinTool
	startedAt      time.Time
	// OIDC auth (optional; if nil, OIDC login is disabled)
	authProviders []auth.AuthProvider
	authSvc       *auth.AuthService
	sessionMgr    *auth.SessionManager
	stateMgr      *authoidc.StateManager
	localAuth     *local.Service
	// baseURL is the public URL for this instance (from STELLA_BASE_URL).
	baseURL string
	// groupSvc owns the Web group/channel application boundary (CRUD, membership,
	// messages, send). Its send path degrades to 503 when the event log or group
	// dispatcher is absent; the read/CRUD path stays available.
	groupSvc *channel.GroupService
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
	// Pinger is the narrow database-liveness port backing the /healthz, /readyz,
	// and admin status probes. The composition root injects the pool as this
	// narrow port so the transport can never reach an application query — every
	// data access is routed through a domain service below.
	Pinger DBPinger

	// ChannelResolver is the narrow runtime read port for webhook/channel and
	// agent-name lookups, replacing the aggregate config.Store on the transport.
	ChannelResolver *channel.RuntimeResolver

	// Account owns the user-account application boundary: admin/self user
	// reads/mutations, login/channel identities, sessions, password credential,
	// and agent assignments, with the role/deactivation revocation invariants.
	Account *account.Service

	// Profile owns the per-(user, agent) memory application boundary: profile,
	// soul, constraints, changelog, list, and reset, with the Agent-access gate
	// and change-source audit. The transport no longer reaches memory.Provider,
	// memorywrite, or the query layer for these.
	Profile *memprofile.Service

	// ProjectStore owns the Authority-bound project use cases (list/create/get/
	// update/delete) with the Agent gate, ownership, route-agent binding, and
	// workspace-containment invariant. Inbox is the cross-Goal/Scheduler inbox
	// read model. Both keep the query layer out of the transport.
	ProjectStore *agent.ProjectStore
	Inbox        *inbox.Service

	// Authorization. AgentAccess and SessionAccess own their domain rules over
	// trusted Authority values and durable state.
	AgentAccess   *agentaccess.Service
	SessionAccess *sessionaccess.Service
	// AgentManagement owns the Agent write use cases (create/update/delete, admin
	// assignment, conversation-activity read model). It layers on AgentAccess for
	// authorization and holds the durable write + runtime-reload ports so the HTTP
	// transport no longer orchestrates them.
	AgentManagement *agentaccess.Management
	// ToolOverrides persists per-agent tool-visibility overrides. The transport
	// holds this narrow domain store instead of the aggregate query handle.
	ToolOverrides *agent.ToolOverrideStore
	// SkillAccess is the DB-backed Skill enforcement point. When nil the
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
	// Group owns the Web group/channel application boundary: authorized CRUD,
	// membership with the last-member invariant, message list, and the send path
	// (command interception, dedup append + outbox claim, synchronous dispatch).
	// It holds the event log and group dispatcher internally, so the transport no
	// longer reaches the query layer or sqlc for groups.
	Group *channel.GroupService
	// Assets is the authoritative asset persistence service. Session workspace
	// handlers use its narrow durable-write/restore/move/delete ports instead of a
	// raw blob store, so the HTTP transport never holds process-global blob state.
	Assets *asset.Store
	// Webhooks resolves public ingress capabilities into fixed owner, Agent, and
	// worker Authority. It is required because /webhooks is always mounted.
	Webhooks *webhook.Service

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

// OIDCDeps groups the login-authentication components produced by oidc.Setup.
// The account identity stores no longer live here: they are composed inside the
// Account service (Deps.Account). LocalAuth is nil in external-OIDC mode.
type OIDCDeps struct {
	Providers  []auth.AuthProvider
	AuthSvc    *auth.AuthService
	SessionMgr *auth.SessionManager
	StateMgr   *authoidc.StateManager
	LocalAuth  *local.Service
}

// validate reports every missing required dependency at once (fail-fast). A
// dependency is required only when the server dereferences it unconditionally
// (no 503 gate). Optional capabilities — CredentialFrontDoor, OAuthAuthServer,
// Vault, VaultRecipient, MCP, Scheduler, Goal, Workflow, and
// OIDC.StateMgr/LocalAuth — are intentionally not checked: a nil there is a
// supported configuration whose endpoints degrade to 503. The Group service is
// required (its own send path degrades internally when the event log or
// dispatcher is absent).
func (d Deps) validate() error {
	var missing []string
	req := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	req(d.Pinger != nil, "Pinger")
	req(d.ChannelResolver != nil, "ChannelResolver")
	req(d.Group != nil, "Group")
	req(d.Account != nil, "Account")
	req(d.Profile != nil, "Profile")
	req(d.ProjectStore != nil, "ProjectStore")
	req(d.Inbox != nil, "Inbox")
	req(d.AgentAccess != nil, "AgentAccess")
	req(d.AgentManagement != nil, "AgentManagement")
	req(d.ToolOverrides != nil, "ToolOverrides")
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
	req(d.Webhooks != nil, "Webhooks")
	req(d.OIDC.AuthSvc != nil, "OIDC.AuthSvc")
	req(d.OIDC.SessionMgr != nil, "OIDC.SessionMgr")
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
		channelResolver: deps.ChannelResolver,
		account:         deps.Account,
		profileSvc:      deps.Profile,
		projectStore:    deps.ProjectStore,
		inboxSvc:        deps.Inbox,
		agentAccess:     deps.AgentAccess,
		agentManagement: deps.AgentManagement,
		toolOverrides:   deps.ToolOverrides,
		sessionAccess:   deps.SessionAccess,
		skillAccess:     deps.SkillAccess,
		rateLimiter:     auth.NewRateLimiter(),
		webhookLimiter:  newWebhookLimiter(5, 20),
		linkCodes:       deps.LinkCodes,
		poolManager:     deps.PoolManager,
		pinger:          deps.Pinger,
		pluginHost:      deps.PluginHost,
		weixinRegistrar: deps.WeixinRegistrar,
		mux:             http.NewServeMux(),
		log:             log,
		baseURL:         deps.BaseURL,
		builtinTools:    append([]agent.BuiltinTool(nil), deps.BuiltinTools...),
		vaultRecipient:  deps.VaultRecipient,
		vaultSvc:        deps.Vault,
		mcpSvc:          deps.MCP,
		credResolver:    deps.CredentialFrontDoor,
		oauthAS:         deps.OAuthAuthServer,
		controlPlane:    deps.ControlPlane,
		credSvc:         deps.Credentials,
		emailSvc:        deps.Email,
		shareSvc:        deps.Share,
		recallySvc:      deps.Recally,
		recally:         newRecallyHandlersWithService(deps.Recally, log),
		schedulerSvc:    deps.Scheduler,
		goalSvc:         deps.Goal,
		workflowSvc:     deps.Workflow,
		webhookIngress:  webhookServiceIngressPort{svc: deps.Webhooks},
		webhookRun:      poolWebhookRunPort{pool: deps.PoolManager},
		groupSvc:        deps.Group,
		assets:          deps.Assets,
		authProviders:   deps.OIDC.Providers,
		authSvc:         deps.OIDC.AuthSvc,
		sessionMgr:      deps.OIDC.SessionMgr,
		stateMgr:        deps.OIDC.StateMgr,
		localAuth:       deps.OIDC.LocalAuth,
		startedAt:       time.Now(),
		runtimeCtx:      ctx,
	}
	// Drain signal is a child of runtimeCtx so a hard process stop also releases
	// streaming handlers. The narrow DBPinger answers the /readyz liveness ping.
	s.readiness = newReadiness(ctx, s.pinger)

	s.registerRoutes()

	return s, nil
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
