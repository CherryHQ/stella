package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"

	"golang.org/x/sync/errgroup"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/observability"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/internal/vault"
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
	if os.Getenv("STELLA_VAULT_KEY") == "" {
		return errors.New(
			"STELLA_VAULT_KEY is not set\n\n" +
				"stella requires a vault key to encrypt credentials and secrets.\n" +
				"Generate one and add it to $STELLA_HOME/.env:\n\n" +
				"  stella vault keygen\n" +
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

	ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)

	startDiagnostics(ctx)

	s, err := setup(ctx, true)
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

	return runServer(s.ctx, s, listFn, switchFn, c.String("host"), c.Int("port"))
}

func runServer(ctx context.Context, s *setupResult, listFn func() []pkgchannel.ModelOption, switchFn func(string, string) error, adminHost string, adminPort int) error {
	g, gctx := errgroup.WithContext(ctx)

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
	adminSrv.SetBaseURL(resolveBaseURL(adminHost, adminPort))
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

	// Wire vault service if STELLA_VAULT_KEY is set.
	var coordOpts []channel.CoordinatorOption
	var tokenSvc *auth.TokenService
	var mcpVault mcp.Vault // nil when the vault is unavailable; MCP bearer auth then rejected
	coordOpts = append(coordOpts, channel.WithCoordinatorAuth(as, engine, linkCodes))
	if vaultKey := os.Getenv("STELLA_VAULT_KEY"); vaultKey != "" {
		vaultSvc, err := vault.NewService(sqlc.New(s.db), vaultKey)
		if err != nil {
			slog.Warn("vault service init failed; vault endpoints will return 503", "error", err)
		} else {
			tokenSvc = auth.NewTokenService(as)
			mcpVault = vaultSvc
			adminSrv.SetVaultService(vaultSvc)
			adminSrv.SetTokenService(tokenSvc)
			adminSrv.SetVaultRecipient(vaultSvc.MasterRecipient())
			s.poolManager.SetVaultEnvLoader(gctx, vaultSvc)
			s.poolManager.SetTokenEnsurer(gctx, tokenSvc)
			coordOpts = append(coordOpts, channel.WithVaultRecipient(vaultSvc.MasterRecipient()))
			coordOpts = append(coordOpts, channel.WithVaultService(vaultSvc))
		}
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
		BaseURL:    resolveBaseURL(adminHost, adminPort),
		VaultKey:   os.Getenv("STELLA_VAULT_KEY"),
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
		<-gctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutCtx)
	})
	g.Go(func() error {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
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
		if err := s.riverClient.Start(ctx); err != nil {
			return fmt.Errorf("start river client: %w", err)
		}
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

	waitErr := g.Wait()
	slog.Info("gateway stopped")
	return waitErr
}

func schedulerJobContext(ctx context.Context, agentID string, job scheduler.Job) context.Context {
	if job.UserID != "" {
		ctx = memory.WithUserID(ctx, job.UserID)
	}
	if agentID != "" {
		ctx = memory.WithAgentID(ctx, agentID)
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

func resolveBaseURL(adminHost string, adminPort int) string {
	if v := os.Getenv("STELLA_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return adminBaseURL(adminHost, adminPort)
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
