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
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"

	"golang.org/x/sync/errgroup"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/internal/vault"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/providers"
	reflectplugin "github.com/CherryHQ/stella/plugins/reflect"
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
				"Generate one with age-keygen and add it to $STELLA_HOME/.env:\n\n" +
				"  age-keygen\n" +
				"  echo 'STELLA_VAULT_KEY=AGE-SECRET-KEY-1...' >> ~/.stella/.env\n\n" +
				"Back up the key — if it is lost, all stored secrets become unrecoverable.\n" +
				"See the vault documentation for details",
		)
	}

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
	return runServer(s.ctx, s, s.modelListFunc(s.snap), s.modelSwitchFunc(s.snap, s.pool), c.String("host"), c.Int("port"))
}

func runServer(ctx context.Context, s *setupResult, listFn func() []pkgchannel.ModelOption, switchFn func(string, string) error, adminHost string, adminPort int) error {
	g, gctx := errgroup.WithContext(ctx)
	channels := make([]pkgchannel.Channel, 0)

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
	adminSrv := server.New(s.store, as, engine, s.mem, s.db, linkCodes, s.poolManager, s.pluginHost)
	if s.schedulerSvc != nil {
		adminSrv.SetSchedulerService(s.schedulerSvc)
	}
	if s.tasksSvc != nil {
		adminSrv.SetTasksService(s.tasksSvc)
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

	if len(channels) == 0 && managedChannels.Started == 0 {
		reason := noRunningChannelReason(managedChannels)
		slog.Warn(reason+"; running Web UI only", "configured_channels", managedChannels.Configured)
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

	if s.tasksSvc != nil {
		if err := s.tasksSvc.Start(ctx); err != nil {
			return fmt.Errorf("start tasks service: %w", err)
		}
		defer s.tasksSvc.Stop()
		if s.schedulerSvc != nil {
			if err := s.schedulerSvc.ScheduleEvery(ctx, "30s", func(ctx context.Context) {
				s.tasksSvc.Tick()
			}); err != nil {
				return fmt.Errorf("schedule tasks tick: %w", err)
			}
		}
	}

	if s.schedulerSvc != nil {
		s.schedulerSvc.SetListActiveUsersFunc(func(ctx context.Context) ([]string, error) {
			users, err := as.ListUsers(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(users))
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
	if job.UserID != "" {
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

func noRunningChannelReason(managedChannels managedChannelRuntimeSummary) string {
	if managedChannels.Configured > 0 {
		return "configured channels failed to start"
	}

	return "no channels configured"
}
