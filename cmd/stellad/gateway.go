package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"

	"golang.org/x/sync/errgroup"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/observability"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/server"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/providers"
)

const defaultAdminPort = 25678

func serverCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "server",
		Aliases:  []string{"serve"},
		Usage:    "Start the stella server",
		Category: "System",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:    "host",
				Usage:   "Host/interface for Web UI",
				Value:   "127.0.0.1",
				EnvVars: []string{"HOST"},
			},
			&ucli.IntFlag{
				Name:    "port",
				Usage:   "Port for Web UI",
				Value:   defaultAdminPort,
				EnvVars: []string{"PORT"},
			},
		},
		Action: serverAction,
	}
}

func serverAction(c *ucli.Context) error {
	// Parse the full server environment once, up front, so a misconfigured
	// value (bad duration, non-boolean guard) fails fast before any subsystem
	// starts. This is the single startup boundary that reads ServerConfig;
	// operator commands that never call setup (version, vault keygen, service,
	// mise) must not reach it, so an unrelated bad variable cannot block them.
	cfg, err := config.LoadServerConfig(os.LookupEnv)
	if err != nil {
		return err
	}

	// The vault key is required to run the server. Check it from the parsed
	// config so there is a single reader; the key is a secret and never appears
	// in this error text.
	if cfg.Vault.Key == "" {
		return errors.New(
			"STELLA_VAULT_KEY is not set\n\n" +
				"stella requires a vault key to encrypt credentials and secrets.\n" +
				"Generate one and add it to $STELLA_HOME/.env:\n\n" +
				"  stellad vault keygen\n" +
				"  echo 'STELLA_VAULT_KEY=AGE-SECRET-KEY-1...' >> ~/.stella/.env\n\n" +
				"Back up the key — if it is lost, all stored secrets become unrecoverable.\n" +
				"See the vault documentation for details",
		)
	}

	// Clean up stale upgrade artifacts (.tmp/.bak/.old) from interrupted upgrades.
	if installDir, err := resolveUpgradeDir(""); err == nil {
		warnStaleUpgradeArtifacts(installDir)
		cleanStaleUpgradeArtifacts(installDir)
	}

	// Signals are handled manually, not via signal.NotifyContext: once serving,
	// the first SIGINT/SIGTERM must start a graceful drain without cancelling
	// work contexts, and only a second signal hard-stops. This base context is
	// cancelled by the cleanup defers below — or by a signal DURING STARTUP,
	// where "abort setup" (stopping the embedded PostgreSQL via the defers, not
	// killing the process with a live postmaster) is the old NotifyContext
	// behavior we must keep. runServer hands the channel over to the drain
	// supervisor once subsystems are up.
	ctx, cancel := context.WithCancel(c.Context)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	startupDone := make(chan struct{})
	var handoff sync.Once
	endStartupWatch := func() { handoff.Do(func() { close(startupDone) }) }
	// Release the watcher even when startup fails before runServer hands off.
	defer endStartupWatch()
	go func() {
		select {
		case <-sigCh:
			slog.Info("shutdown signal during startup; aborting")
			cancel()
		case <-startupDone:
		}
	}()

	startDiagnostics(ctx, cfg.Diagnostics.PprofAddr)

	s, err := setup(ctx, cfg)
	if err != nil {
		cancel()
		return err
	}

	// Both cleanup defers are registered before observability.Init so a failed
	// Init still drains setup's resources (pools, background tasks). obs is a
	// nil-safe Provider until assigned, so its Shutdown is a no-op if Init never
	// ran. The shutdown defer is registered FIRST so it runs LAST (LIFO): only
	// after poolManager.Close() → tracehook.Close() → endSession() has ended
	// every in-flight session span do we flush and stop the provider, otherwise
	// those end-of-session spans land on a stopped provider and get dropped.
	var obs *observability.Provider
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		if err := obs.Shutdown(shutCtx); err != nil {
			slog.Warn("otel shutdown failed", "error", err)
		}
	}()
	defer func() {
		cancel()
		s.waitBackgroundTasks()
		_ = s.poolManager.Close()
		// Stop the managed PostgreSQL last, once every DB user is done: close the
		// pool first so the server shuts down without active connections. Only set
		// in zero-config mode; an external DSN leaves s.embedded nil.
		if s.embedded != nil {
			s.db.Close()
			_ = s.embedded.Stop()
		}
	}()

	// Initialize global OTel tracing before any component creates spans.
	obs, err = observability.Init(ctx)
	if err != nil {
		return fmt.Errorf("init observability: %w", err)
	}

	listFn := func() []pkgchannel.ModelOption {
		return collectModelsFromStore(s.ctx, s.store)
	}
	switchFn := func(_, _ string) error { return nil }

	return runServer(s.ctx, s, listFn, switchFn, c.String("host"), c.Int("port"), sigCh, endStartupWatch)
}

// runServer starts every subsystem and blocks until shutdown. sigCh delivers
// SIGINT/SIGTERM (registered by the caller, who owns signal.Stop); onServing is
// called exactly once, when startup is complete and the two-phase drain
// supervisor has taken over signal handling from the caller's startup watcher.
func runServer(ctx context.Context, s *setupResult, listFn func() []pkgchannel.ModelOption, switchFn func(string, string) error, adminHost string, adminPort int, sigCh <-chan os.Signal, onServing func()) error {
	// workCtx is the errgroup parent. Graceful drain cancels it only AFTER the
	// HTTP server has drained, so the LIFO defer chain (goal tick/dispatcher,
	// scheduler, riverClient.Stop) then tears subsystems down in order. A
	// subsystem crash still cancels gctx via the errgroup, exactly as before.
	// River is started below on a context decoupled from workCtx, so in-flight
	// jobs survive this cancellation and drain under the soft-stop budget.
	workCtx, workCancel := context.WithCancel(ctx)
	defer workCancel()
	g, gctx := errgroup.WithContext(workCtx)

	warnDeploymentBaseURL(resolveBaseURL(s.cfg.BaseURL, adminHost, adminPort), s.cfg.OIDC.IssuerURL)

	// Seed default data (channels, providers, default agent) if absent.
	if err := s.store.Seed(gctx); err != nil {
		return fmt.Errorf("seed default data: %w", err)
	}

	// Create auth store and policy engine for channel bots and Web UI.
	as := appdb.NewAuthStore(s.db)
	engine, err := auth.NewEngine(gctx, as)
	if err != nil {
		return fmt.Errorf("create auth engine: %w", err)
	}

	// Link codes are shared between Web UI and channel bots.
	linkCodes, err := auth.NewSharedLinkCodeStore(gctx, s.db)
	if err != nil {
		return fmt.Errorf("create link code store: %w", err)
	}

	// Admin server is always created so channel stop functions can be registered
	// even when the panel is disabled.
	adminSrv := server.New(gctx, s.store, as, engine, s.mem, s.db, linkCodes, s.poolManager, s.pluginHost)
	adminSrv.SetBuiltinTools(s.builtinTools)
	if s.credSvc != nil {
		adminSrv.SetCredentialsService(s.credSvc)
	}
	if s.shareSvc != nil {
		adminSrv.SetShareService(s.shareSvc)
	}
	if s.recallySvc != nil {
		adminSrv.SetRecallyService(s.recallySvc)
	}
	adminSrv.SetBaseURL(resolveBaseURL(s.cfg.BaseURL, adminHost, adminPort))
	if s.schedulerSvc != nil {
		adminSrv.SetSchedulerService(s.schedulerSvc)
	}
	if s.goalSvc != nil {
		adminSrv.SetGoalService(s.goalSvc)
	}
	if s.workflowSvc != nil {
		adminSrv.SetWorkflowService(s.workflowSvc)
	}

	// Wire the shared credentials service: inject invalidator so token
	// refresh propagates to running pools.
	credSvc := adminSrv.CredentialsService()
	credSvc.SetInvalidator(s.poolManager)
	if s.oauthRegistry != nil {
		credSvc.SetRegistry(s.oauthRegistry)
		s.poolManager.SetOAuthRegistry(s.oauthRegistry)
	}

	adminSrv.InitCredentialFrontDoor()

	// Wire vault service if STELLA_VAULT_KEY was valid during setup.
	var coordOpts []channel.CoordinatorOption
	var mcpVault mcp.Vault // nil when the vault is unavailable; MCP bearer auth then rejected
	coordOpts = append(coordOpts, channel.WithCoordinatorAuth(as, engine, linkCodes))
	if s.vaultSvc != nil {
		mcpVault = s.vaultSvc
		adminSrv.SetVaultService(s.vaultSvc)
		adminSrv.SetVaultRecipient(s.vaultSvc.MasterRecipient())
		s.poolManager.SetVaultEnvLoader(gctx, s.vaultSvc)
		coordOpts = append(coordOpts, channel.WithVaultRecipient(s.vaultSvc.MasterRecipient()))
		coordOpts = append(coordOpts, channel.WithVaultService(s.vaultSvc))
	}

	// Wire the MCP client: one registration service shared by the HTTP API
	// (add/list/remove) and the agent runtime (tool exposure). Servers using
	// auth_type=none work even without the vault; bearer auth requires it.
	mcpSvc := mcp.NewService(sqlc.New(s.db), mcpVault)
	adminSrv.SetMCPService(mcpSvc)
	s.poolManager.SetMCPToolProvider(gctx, mcp.NewToolProvider(mcpSvc))

	// Wire authentication (external OIDC/OAuth providers and local password auth).
	oidcStore := appdb.NewOIDCStore(s.db)
	oidcResult, err := oidc.Setup(gctx, oidc.SetupParams{
		DB:         s.db,
		BaseURL:    resolveBaseURL(s.cfg.BaseURL, adminHost, adminPort),
		VaultKey:   s.cfg.Vault.Key,
		OIDC:       s.cfg.OIDC,
		AuthStores: oidcStore,
	})
	if err != nil {
		return fmt.Errorf("oidc: setup: %w", err)
	}
	adminSrv.SetLoginIdentityStore(oidcStore)
	adminSrv.SetUserStore(oidcStore)
	adminSrv.SetSessionStore(oidcStore)
	adminSrv.SetCredentialStore(oidcStore)
	adminSrv.SetOIDCAuth(oidcResult)
	slog.Info("oidc: authentication configured")

	intentClassifier := newIntentClassifier(s.store, s.pluginHost)
	coordOpts = append(coordOpts, channel.WithIntentClassifier(intentClassifier))

	if semanticArbiter := newSemanticGroupArbiter(s.store, s.pluginHost); semanticArbiter != nil {
		coordOpts = append(coordOpts, channel.WithSemanticGroupArbiter(semanticArbiter))
	}

	elStore := eventlog.NewStore(s.db)
	adminSrv.SetEventLogStore(elStore)
	adminSrv.SetArbiter(channel.NewArbiter(channel.ArbiterConfig{
		MaxRepliesPerTrigger: 100,
	}))
	botRegistry := channel.NewBotIdentityRegistry()
	publisherRegistry := channel.NewPublisherRegistry()
	coordOpts = append(coordOpts, channel.WithDB(s.db))
	coordOpts = append(coordOpts, channel.WithEventLog(elStore))
	coordOpts = append(coordOpts, channel.WithBotRegistry(botRegistry))
	coordOpts = append(coordOpts, channel.WithPublisherRegistry(publisherRegistry))
	coordOpts = append(coordOpts, channel.WithArbiter(channel.NewArbiter(channel.ArbiterConfig{
		MaxRepliesPerTrigger: 1,
	})))
	coordOpts = append(coordOpts, channel.WithGroupMemberLister(
		channel.FuncGroupMemberLister(func(ctx context.Context, groupID string) ([]channel.GroupMember, error) {
			rows, err := sqlc.New(s.db).ListGroupMembers(ctx, groupID)
			if err != nil {
				return nil, err
			}
			members := make([]channel.GroupMember, len(rows))
			for i, r := range rows {
				members[i] = channel.GroupMember{AgentID: r.AgentID, ReplyChannelID: r.ReplyChannelID}
			}
			return members, nil
		}),
	))

	// Create the coordinator that implements MessageHandler for all channels.
	coordinator := channel.NewCoordinator(
		s.poolManager,
		s.store,
		listFn,
		switchFn,
		coordOpts...,
	)
	groupDispatcher := channel.NewGroupDispatcher(s.db, coordinator, publisherRegistry)
	coordinator.SetGroupDispatcher(groupDispatcher)
	adminSrv.SetGroupDispatcher(groupDispatcher)
	g.Go(func() error {
		if err := groupDispatcher.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})
	if s.channelRuntimeServices != nil {
		s.channelRuntimeServices.Set(gctx, coordinator, s.notifier)
	}

	// Apply managed channel plugins at startup.
	applyManagedChannelPlugins(gctx, s.pluginHost)

	// Start Web UI server.
	listenAddr := adminListenAddress(adminHost, adminPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("admin listen: %w", err)
	}
	addr := ln.Addr().String()
	slog.Info("starting Web UI", "addr", addr)
	fmt.Printf("Web UI running at %s\n", adminURLForDisplay(adminHost, adminPort, addr))

	httpSrv := &http.Server{Handler: adminSrv.Handler()}
	g.Go(func() error {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("admin serve: %w", err)
		}
		return nil
	})

	// Wire auth directory into dispatcher for per-user notification routing.
	s.notifier.SetAuthService(s.pluginHost.Auth())

	// Start the single shared River client (composition root: buildSharedRiverClient
	// assembled it from the scheduler and goal queues). It is started before the
	// subsystems and, because defers run LIFO, its Stop runs last — after the goal
	// tick and the scheduler have stopped — so in-flight attempt and scheduled jobs
	// drain gracefully with no new work being enqueued.
	if s.riverClient != nil {
		// Decouple River from workCtx: graceful drain cancels workCtx/gctx, but
		// in-flight goal/scheduler agent runs must keep executing until Stop drains
		// them within the soft-stop budget. WithoutCancel preserves values (tracing)
		// while dropping cancellation.
		if err := s.riverClient.Start(context.WithoutCancel(workCtx)); err != nil {
			return fmt.Errorf("start river client: %w", err)
		}
		// Stop waits for in-flight jobs, then cancels their contexts after
		// SoftStopTimeout (STELLA_RIVER_SOFT_STOP_TIMEOUT); River logs the jobs it
		// force-cancels. A background context means we wait for that full escalation
		// rather than giving up early.
		//
		// drain ceiling: a river job that completes after gctx is cancelled may fail
		// its downstream delivery (notifier/channel runtime captured gctx); the goal
		// lease-reaper / outbox retry heals it. Perfect ordering would move subsystem
		// teardown out of these defers -- deferred to the multi-replica Phase 2 when
		// it actually matters.
		defer func() { _ = s.riverClient.Stop(context.Background()) }()
	}

	// Wire scheduler and start it. In external-river mode Start/Stop register and
	// tear down the scheduler's periodic and one-time jobs against the shared
	// client but do not start or stop it.
	if s.schedulerSvc != nil {
		adminSrv.SetSchedulerService(s.schedulerSvc)
		wireSchedulerCallbacks(s.schedulerSvc, s.poolManager, s.notifier)
		if err := s.schedulerSvc.Start(ctx); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		s.schedulerSvc.EnsureBuiltinJobs()
		defer func() { _ = s.schedulerSvc.Stop() }()
	}

	// Goal execution substrate (River Phase 2a + 2b). The dispatcher enqueues
	// claimed attempts onto the shared client (injected via SetRiverClient); its
	// convergence tick runs as a single-leader River periodic job (StartDispatchTick)
	// rather than a per-node in-process ticker, so the cluster runs ONE convergence
	// loop instead of every node scanning redundantly.
	// Shutdown (defers run LIFO): remove the periodic and quiet the dispatcher
	// first so no new ticks/claims enqueue, then the scheduler, then drain
	// in-flight jobs when the shared client stops.
	if s.goalSvc != nil && s.riverClient != nil {
		tick, err := s.goalSvc.StartDispatchTick()
		if err != nil {
			return fmt.Errorf("start goal dispatcher tick: %w", err)
		}
		// LIFO: quiet the dispatcher BEFORE removing the periodic, so any tick job
		// already queued that a worker picks up during shutdown finds the dispatcher
		// stopped and no-ops instead of claiming fresh work.
		defer s.goalSvc.StopDispatchTick(tick)
		defer s.goalSvc.Dispatcher.Stop()
	}

	// Embedding backfill periodic: a single-leader River job that drains the
	// embedding backlog (RunOnStart kicks an initial pass). Registered against the
	// same shared client; the defer removes it so no further firings enqueue on
	// shutdown. Only present when the semantic lane is configured.
	if s.embeddingSvc != nil && s.riverClient != nil {
		handle, err := s.embeddingSvc.StartBackfill()
		if err != nil {
			return fmt.Errorf("start embedding backfill: %w", err)
		}
		defer s.embeddingSvc.StopBackfill(handle)
	}

	// Two-phase shutdown supervisor (runs OUTSIDE the errgroup). The first
	// SIGINT/SIGTERM starts a graceful drain; a second collapses to a hard stop.
	// A subsystem crash cancels gctx and is torn down without a readiness drain.
	// Started only now, with every subsystem up: a signal at any earlier point is
	// consumed by serverAction's startup watcher, which cancels the base context
	// so partially-started subsystems unwind through the error path — the
	// pre-existing abort-startup semantics. onServing hands signal ownership over.
	drainer := &drainSequence{
		beginDrain:   adminSrv.BeginDrain,
		httpTimeout:  s.cfg.Lifecycle.HTTPShutdownTimeout,
		shutdownHTTP: httpSrv.Shutdown,
		forceClose:   func() { _ = httpSrv.Close() },
		cancelWork:   workCancel,
	}
	onServing()
	go superviseShutdown(gctx, sigCh, httpSrv, drainer)

	// All subsystems are started; /readyz may now report ready. Do this last,
	// immediately before blocking on the errgroup, so a probe never sees ready
	// while wiring is still in progress.
	adminSrv.MarkStartupComplete()

	waitErr := g.Wait()
	slog.Info("gateway stopped")
	return waitErr
}

// drainSequence runs the graceful shutdown steps in order. It is a struct of
// side-effect hooks so the ordering can be asserted in tests without a live
// server. The order is: flip not-ready + signal SSE (beginDrain) -> HTTP
// shutdown within the budget -> force-close leftovers -> cancel work contexts.
//
// There is deliberately no in-process delay between not-ready and shutdown for
// load-balancer propagation: that window is the platform's job (Kubernetes
// preStop sleep), not the process's.
type drainSequence struct {
	beginDrain   func()
	httpTimeout  time.Duration
	shutdownHTTP func(context.Context) error
	forceClose   func()
	cancelWork   func()
}

func (d *drainSequence) run() {
	// 1. Flip readiness to not-ready and signal SSE streams to end. This is
	//    observable before the listener is touched (happens-before), so a probe
	//    can never see /readyz succeed once the drain has begun.
	d.beginDrain()
	// 2. Stop accepting and drain in-flight HTTP within the budget; force-close
	//    anything still open when the budget is spent.
	shutCtx, cancel := context.WithTimeout(context.Background(), d.httpTimeout)
	defer cancel()
	if err := d.shutdownHTTP(shutCtx); err != nil {
		d.forceClose()
	}
	// 3. Cancel work contexts: gctx cancels, g.Wait returns, and the LIFO defer
	//    chain drains the subsystems and River.
	d.cancelWork()
}

// superviseShutdown blocks until the first signal (start graceful drain) or a
// subsystem crash cancelling gctx (hard teardown, no drain — prior semantics).
// During the drain a second signal aborts the remaining budget and hard-stops
// immediately.
func superviseShutdown(gctx context.Context, sigCh <-chan os.Signal, httpSrv *http.Server, d *drainSequence) {
	select {
	case <-sigCh:
		// Graceful drain below.
		slog.Info("shutdown signal received; starting graceful drain")
	case <-gctx.Done():
		// A subsystem error cancelled the errgroup — not a drain. Mirror the prior
		// <-gctx.Done() -> Shutdown(2s) path: bounded HTTP shutdown, no readiness
		// drain and no drain budget, so Serve returns and g.Wait completes.
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return
	}
	// Watch for a second signal: force-closing the server aborts the in-flight
	// Shutdown wait, so the drain collapses to an immediate hard stop.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			slog.Warn("second shutdown signal received; hard-stopping")
			_ = httpSrv.Close()
		case <-done:
		}
	}()
	d.run()
}

func schedulerJobContext(ctx context.Context, agentID string, job scheduler.Job) context.Context {
	if job.UserID != "" {
		ctx = authz.WithUserID(ctx, job.UserID)
	}
	if agentID != "" {
		ctx = authz.WithAgentID(ctx, agentID)
	}
	ctx = agent.WithExcludedTools(ctx, "scheduler")
	return ctx
}

func schedulerJobMessage(job scheduler.Job) string {
	return fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s\n\nUse the notify tool to send results to the user only when you have something meaningful to communicate.", job.Name, job.Message)
}

func adminListenAddress(host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

func adminBaseURL(host string, port int) string {
	h := host
	if h == "" {
		h = "localhost"
	}
	return "http://" + net.JoinHostPort(h, fmt.Sprintf("%d", port))
}

// resolveBaseURL returns the canonical base URL: the configured STELLA_BASE_URL
// (threaded in raw as baseURL, trailing slash trimmed here) when set, otherwise
// one derived from the admin bind host.
func resolveBaseURL(baseURL, adminHost string, adminPort int) string {
	if v := baseURL; v != "" {
		return strings.TrimRight(v, "/")
	}
	return adminBaseURL(adminHost, adminPort)
}

// warnDeploymentBaseURL warns when the canonical base URL used for OAuth
// callbacks and channel deep links cannot work off-box. When STELLA_BASE_URL is
// unset the URL is derived from the bind host, so a default (loopback) bind
// yields a base URL that points back at this machine. That is legitimate in
// every deployment shape (local browser, docker port publish, kubectl
// port-forward) and its failure mode is immediately visible in the login
// redirect — so this warns loudly instead of failing, and only when a feature
// that emits such links is actually configured. Kubernetes charts enforce
// STELLA_BASE_URL as a required value at the layer that knows it is behind an
// ingress.
func warnDeploymentBaseURL(baseURL, oidcIssuerURL string) {
	if !baseURLUnsafe(baseURL) {
		return
	}
	if linkDependentFeaturesConfigured(oidcIssuerURL) {
		slog.Warn("STELLA_BASE_URL is loopback/unspecified; OAuth callbacks and channel deep links will point back at this host and fail off-box. Set STELLA_BASE_URL to the public URL clients use", "base_url", baseURL)
	}
}

// baseURLUnsafe reports whether a base URL cannot serve as a public canonical
// address: it fails to parse, is not http(s), or resolves to a loopback,
// unspecified, or localhost host.
func baseURLUnsafe(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return true
	}
	host := u.Hostname()
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// linkDependentFeaturesConfigured reports whether a feature that emits absolute
// links back to this server (external OIDC or an OAuth login provider) is
// configured. The OIDC signal is the same OIDC_ISSUER_URL snapshot value that
// drives the setup mode decision, so both agree; the dynamic AUTH_OAUTH_* probe
// stays in the auth package.
func linkDependentFeaturesConfigured(oidcIssuerURL string) bool {
	return oidcIssuerURL != "" || oidc.OAuthConfiguredFromEnv()
}

func adminURLForDisplay(host string, port int, fallbackAddr string) string {
	displayHost := host
	if displayHost == "" {
		displayHost = hostFromAddr(fallbackAddr)
	}
	if displayHost == "" {
		displayHost = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(displayHost, fmt.Sprintf("%d", port))
}

func hostFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return host
}

func newIntentClassifier(store config.Store, ph *pluginhost.Host) *channel.LLMIntentClassifier {
	if store == nil || ph == nil {
		return nil
	}
	return channel.NewLLMIntentClassifier(
		func(ctx context.Context, agentID string) (*config.Snapshot, error) {
			return store.Snapshot(ctx, agentID)
		},
		intentClassifierStreamFuncBuilder(ph),
	)
}

func intentClassifierStreamFuncBuilder(ph *pluginhost.Host) channel.StreamFuncBuilder {
	return func(_ context.Context, providerType string, creds config.ProviderCreds) (providers.StreamFunc, error) {
		return ph.BuildStreamFunc(providerType, map[string]any{
			"api_key":  creds.APIKey,
			"base_url": creds.BaseURL,
		})
	}
}

func newSemanticGroupArbiter(store config.Store, ph *pluginhost.Host) *channel.LLMSemanticGroupArbiter {
	if store == nil || ph == nil {
		return nil
	}
	return channel.NewLLMSemanticGroupArbiter(
		func(ctx context.Context, agentID string) (*config.Snapshot, error) {
			return store.Snapshot(ctx, agentID)
		},
		intentClassifierStreamFuncBuilder(ph),
	)
}
