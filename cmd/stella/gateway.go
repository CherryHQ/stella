package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"

	"golang.org/x/sync/errgroup"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/credentials"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/vault"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
	reflectplugin "github.com/CherryHQ/stella/plugins/reflect"
	"github.com/CherryHQ/stella/server"
)

const defaultAdminPort = 25678

func serverFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.StringFlag{
			Name:    "host",
			Aliases: []string{"admin-host"},
			Usage:   "Host/interface for admin panel",
			Value:   "127.0.0.1",
			EnvVars: []string{"HOST"},
		},
		&ucli.IntFlag{
			Name:    "port",
			Aliases: []string{"admin-port"},
			Usage:   "Port for admin panel (0 = disabled)",
			Value:   defaultAdminPort,
			EnvVars: []string{"PORT"},
		},
		&ucli.BoolFlag{
			Name:  "open",
			Usage: "Open admin panel in browser on startup",
		},
	}
}

func serverAction(c *ucli.Context) error {
	ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)

	s, err := setup(ctx, true)
	if err != nil {
		cancel()
		return err
	}
	defer func() {
		cancel()
		s.waitBackgroundTasks()
		_ = s.poolManager.Close()
	}()
	return runServer(s.ctx, s, s.modelListFunc(s.snap), s.modelSwitchFunc(s.snap, s.pool), c.String("host"), c.Int("port"), c.Bool("open"))
}

func runServer(ctx context.Context, s *setupResult, listFn func() []pkgchannel.ModelOption, switchFn func(string, string) error, adminHost string, adminPort int, openBrowser bool) error {
	g, gctx := errgroup.WithContext(ctx)
	var channels []pkgchannel.Channel

	// Create auth store and policy engine for channel bots and admin panel.
	as := appdb.NewAuthStore(s.db)
	engine, err := auth.NewEngine(gctx, as)
	if err != nil {
		return fmt.Errorf("create auth engine: %w", err)
	}

	// Link codes are shared between admin panel and channel bots.
	linkCodes, err := auth.NewSharedLinkCodeStore(gctx, s.db)
	if err != nil {
		return fmt.Errorf("create link code store: %w", err)
	}

	// Admin server is always created so channel stop functions can be registered
	// even when the panel is disabled.
	adminSrv := server.New(s.store, as, engine, s.mem, s.db, linkCodes, s.poolManager, s.pluginHost)
	if s.schedulerSvc != nil {
		adminSrv.SetSchedulerService(s.schedulerSvc)
	}

	// Wire the shared credentials service: inject invalidator and add the tool to
	// all pools so every agent can use the credentials tool.
	credSvc := adminSrv.CredentialsService()
	credSvc.SetInvalidator(s.poolManager)
	if s.oauthRegistry != nil {
		credSvc.SetRegistry(s.oauthRegistry)
		credSvc.SetProviderPluginIDs(s.providerPluginIDs)
		s.poolManager.SetOAuthRegistry(s.oauthRegistry)
	}
	for _, tool := range []tools.Tool{credentials.NewOAuthTool(credSvc), credentials.NewVaultTool(credSvc)} {
		if err := s.poolManager.AddBuiltinTool(gctx, tool); err != nil {
			slog.Warn("failed to add builtin tool to pool manager", "tool", tool.Definition().Name, "error", err)
		}
	}

	// Wire vault service if STELLA_VAULT_KEY is set.
	var coordOpts []channel.CoordinatorOption
	coordOpts = append(coordOpts, channel.WithCoordinatorAuth(as, engine, linkCodes))
	if vaultKey := os.Getenv("STELLA_VAULT_KEY"); vaultKey != "" {
		vaultSvc, err := vault.NewService(sqlc.New(s.db), vaultKey)
		if err != nil {
			slog.Warn("vault service init failed; vault endpoints will return 503", "error", err)
		} else {
			tokenSvc := auth.NewTokenService(as, vaultSvc)
			adminSrv.SetVaultService(vaultSvc)
			adminSrv.SetTokenService(tokenSvc)
			adminSrv.SetVaultRecipient(vaultSvc.MasterRecipient())
			s.poolManager.SetVaultEnvLoader(gctx, vaultSvc)
			s.poolManager.SetTokenService(gctx, tokenSvc)
			coordOpts = append(coordOpts, channel.WithVaultRecipient(vaultSvc.MasterRecipient()))
			coordOpts = append(coordOpts, channel.WithVaultService(vaultSvc))
			n, err := vault.BackfillUserKeys(gctx, sqlc.New(s.db), vaultSvc.MasterRecipient())
			if err != nil {
				slog.Warn("vault: backfill user keys failed", "error", err)
			} else if n > 0 {
				slog.Info("vault: backfilled age keys for users", "count", n)
			}
		}
	}

	intentClassifier := newIntentClassifier(s.store, s.pluginHost)
	coordOpts = append(coordOpts, channel.WithIntentClassifier(intentClassifier))

	// Create the coordinator that implements MessageHandler for all channels.
	coordinator := channel.NewCoordinator(
		s.poolManager,
		s.store,
		listFn,
		switchFn,
		coordOpts...,
	)
	if s.channelRuntimeServices != nil {
		s.channelRuntimeServices.Set(gctx, coordinator, s.notifier)
	}

	managedChannels := applyManagedChannelPlugins(gctx, s.pluginHost)

	// Start admin panel server.
	if adminPort > 0 {
		listenAddr := adminListenAddress(adminHost, adminPort)
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("admin listen: %w", err)
		}
		addr := ln.Addr().String()
		slog.Info("starting admin panel", "addr", addr)
		fmt.Printf("Admin panel running at %s\n", adminURLForDisplay(adminHost, adminPort, addr))

		if openBrowser {
			launchBrowser(adminBrowserURL(adminHost, adminPort, addr))
		}

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
	}

	if len(channels) == 0 && managedChannels.Started == 0 {
		reason := noRunningChannelReason(managedChannels)
		if adminPort > 0 {
			slog.Warn(reason+"; running admin panel only", "configured_channels", managedChannels.Configured)
		} else {
			return fmt.Errorf("no services to run: %s and admin panel disabled", reason)
		}
	}

	// Start all channels.
	for _, ch := range channels {
		g.Go(func() error {
			if err := ch.Start(gctx); err != nil && gctx.Err() == nil {
				return fmt.Errorf("%s: %w", ch.Name(), err)
			}
			return nil
		})
	}

	// Managed channel runtimes are started by plugin application rather than by the
	// legacy channels slice. When the admin panel is disabled, keep the gateway alive
	// until shutdown so managed runtimes can continue serving traffic.
	if adminPort == 0 && managedChannels.Started > 0 {
		g.Go(func() error {
			<-gctx.Done()
			return nil
		})
	}

	// Wire auth directory into dispatcher for per-user notification routing.
	s.notifier.SetAuthService(s.pluginHost.Auth())

	// Wire scheduler notifications and start the scheduler AFTER channels
	// are registered, so early-firing jobs already use the dispatcher.
	if s.schedulerSvc != nil {
		adminSrv.SetSchedulerService(s.schedulerSvc)
		wireSchedulerNotifier(s.schedulerSvc, s.poolManager, s.pool)
		if err := s.schedulerSvc.Start(ctx); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		defer func() { _ = s.schedulerSvc.Stop() }()
	}

	if s.schedulerSvc != nil {
		s.schedulerSvc.SetListActiveUsersFunc(func(ctx context.Context) ([]int64, error) {
			users, err := as.ListUsers(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(users))
			for _, u := range users {
				if u.IsActive {
					ids = append(ids, u.ID)
				}
			}
			return ids, nil
		})
		s.schedulerSvc.EnsureBuiltinJobs()
	}

	if s.snap.Heartbeat.IsEnabled() && s.schedulerSvc != nil {
		if err := s.schedulerSvc.StartHeartbeat(ctx, s.snap.Heartbeat.Interval()); err != nil {
			return fmt.Errorf("schedule heartbeat: %w", err)
		}
	}

	if err := s.pluginHost.ApplyPlugin(gctx, reflectplugin.PluginID); err != nil {
		return fmt.Errorf("apply reflect runtime: %w", err)
	}

	waitErr := g.Wait()
	slog.Info("gateway stopped")
	return waitErr
}

// wireSchedulerNotifier overrides the scheduler callback to run the agent.
// The agent decides whether to notify the user by calling the notify tool.
func wireSchedulerNotifier(schedulerSvc *scheduler.Service, poolMgr *agent.PoolManager, defaultPool *agent.Pool) {
	schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) error {
		if scheduler.IsPluginJob(job) {
			return nil
		}

		pool := schedulerPool(job, poolMgr, defaultPool)
		sessionID := scheduler.RunSessionIDFromContext(ctx)
		if sessionID == "" {
			sessionID = job.SessionID()
		}
		msg := schedulerJobMessage(job)

		jobCtx := schedulerJobContext(ctx, pool, job)

		for evt := range pool.Chat(jobCtx, sessionID, msg) {
			if evt.Err != nil {
				slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
			}
		}
		return nil
	})
}

func schedulerPool(job scheduler.Job, poolMgr *agent.PoolManager, defaultPool *agent.Pool) *agent.Pool {
	if job.AgentID == "" {
		return defaultPool
	}

	pool := poolMgr.Get(job.AgentID)
	if pool != nil {
		return pool
	}

	slog.Warn("scheduler job references unknown agent, using default pool",
		"job_id", job.ID, "agent_id", job.AgentID)
	return defaultPool
}

func schedulerJobContext(ctx context.Context, pool *agent.Pool, job scheduler.Job) context.Context {
	if job.UserID != 0 {
		ctx = memory.WithUserID(ctx, job.UserID)
	}
	if pool.AgentID() != "" {
		ctx = memory.WithAgentID(ctx, pool.AgentID())
	}
	// Prevent scheduled jobs from mutating scheduler control-plane state.
	ctx = agent.WithExcludedTools(ctx, "scheduler")
	return ctx
}

func schedulerJobMessage(job scheduler.Job) string {
	return fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s\n\nUse the notify tool to send results to the user only when you have something meaningful to communicate.", job.Name, job.Message)
}

func launchBrowser(url string) {
	cmd := browserCommand(url)
	if cmd == nil {
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("failed to open browser", "error", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func adminListenAddress(host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
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

func adminBrowserURL(host string, port int, fallbackAddr string) string {
	browserHost := host
	if browserHost == "" {
		browserHost = hostFromAddr(fallbackAddr)
	}
	if browserHost == "" || browserHost == "0.0.0.0" || browserHost == "::" {
		browserHost = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(browserHost, fmt.Sprintf("%d", port))
}

func hostFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return host
}

func browserCommand(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "linux":
		return exec.Command("xdg-open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return nil
	}
}

func newIntentClassifier(store config.Store, ph *pluginhost.Host) *channel.LLMIntentClassifier {
	if store == nil || ph == nil {
		return nil
	}
	return channel.NewLLMIntentClassifier(
		func(ctx context.Context, agentID string) (*config.Snapshot, error) {
			return store.Snapshot(ctx, agentID)
		},
		intentClassifierProviderGetterBuilder(ph),
	)
}

func intentClassifierProviderGetterBuilder(ph *pluginhost.Host) channel.ProviderGetterBuilder {
	return func(_ context.Context, providerType string, creds config.ProviderCreds) (providers.ProviderGetter, error) {
		return ph.BuildProviderRegistry(providerType, map[string]any{
			"api_key":  creds.APIKey,
			"base_url": creds.BaseURL,
		})
	}
}

func noRunningChannelReason(managedChannels managedChannelRuntimeSummary) string {
	if managedChannels.Configured > 0 {
		return "configured channels failed to start"
	}

	return "no channels configured"
}
