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
	"strings"
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"

	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/notify"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/internal/vault"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/providers"
	reflectplugin "github.com/vaayne/anna/plugins/reflect"
	"golang.org/x/sync/errgroup"
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
	defer cancel()

	s, err := setup(ctx, true)
	if err != nil {
		return err
	}
	defer func() { _ = s.poolManager.Close() }()
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
	adminSrv := admin.New(s.store, as, engine, s.mem, s.db, linkCodes, s.poolManager, s.pluginHost)

	// Wire vault service if ANNA_VAULT_KEY is set.
	var coordOpts []channel.CoordinatorOption
	coordOpts = append(coordOpts, channel.WithCoordinatorAuth(as, engine, linkCodes))
	if vaultKey := os.Getenv("ANNA_VAULT_KEY"); vaultKey != "" {
		vaultSvc, err := vault.NewService(sqlc.New(s.db), vaultKey)
		if err != nil {
			slog.Warn("vault service init failed; vault endpoints will return 503", "error", err)
		} else {
			adminSrv.SetVaultService(vaultSvc)
			adminSrv.SetVaultRecipient(vaultSvc.MasterRecipient())
			s.poolManager.SetVaultEnvLoader(gctx, vaultSvc)
			coordOpts = append(coordOpts, channel.WithVaultRecipient(vaultSvc.MasterRecipient()))
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

	if managedChannels.Started > 0 && len(s.notifier.Channels()) == 0 {
		slog.Warn("no enabled channels have enable_notify set to true; scheduler results and heartbeat notifications will not be delivered")
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
		wireSchedulerNotifier(s.schedulerSvc, s.poolManager, s.pool, s.notifier)
		if err := s.schedulerSvc.Start(ctx); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		defer func() { _ = s.schedulerSvc.Stop() }()
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

// wireSchedulerNotifier overrides the scheduler callback to collect the agent response
// and dispatch it via the notification dispatcher. User-owned jobs notify only their
// owner; system jobs (user_id=0) broadcast to all channels.
func wireSchedulerNotifier(schedulerSvc *scheduler.Service, poolMgr *agent.PoolManager, defaultPool *agent.Pool, dispatcher *notify.Dispatcher) {
	schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) error {
		if scheduler.IsPluginJob(job) {
			return nil
		}

		pool := schedulerPool(job, poolMgr, defaultPool)
		sessionID := job.SessionID()
		msg := schedulerJobMessage(job)

		jobCtx := schedulerJobContext(ctx, pool, job)

		var result strings.Builder
		for evt := range pool.Chat(jobCtx, sessionID, msg) {
			if evt.Err != nil {
				slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
			}
			if evt.Text != "" {
				result.WriteString(evt.Text)
			}
		}
		if result.Len() == 0 {
			return nil
		}

		n := pkgchannel.Notification{
			AgentID: pool.AgentID(),
			Text:    fmt.Sprintf("*%s*\n\n%s", job.Name, result.String()),
		}
		err := dispatchSchedulerNotification(ctx, dispatcher, job, n)
		if err != nil {
			slog.Error("scheduler notification failed", "job_id", job.ID, "error", err)
		}
		return err
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

func dispatchSchedulerNotification(ctx context.Context, dispatcher *notify.Dispatcher, job scheduler.Job, notification pkgchannel.Notification) error {
	if job.UserID != 0 {
		return dispatcher.NotifyUser(ctx, job.UserID, notification)
	}

	return dispatcher.Notify(ctx, notification)
}

func schedulerJobContext(ctx context.Context, pool *agent.Pool, job scheduler.Job) context.Context {
	if job.UserID != 0 {
		ctx = memory.WithUserID(ctx, job.UserID)
	}
	if pool.AgentID() != "" {
		ctx = memory.WithAgentID(ctx, pool.AgentID())
	}
	// Scheduled executions already have an external delivery path and should not
	// mutate scheduler control-plane state while they run.
	ctx = runner.WithExcludedTools(ctx, "notify", "scheduler")
	return ctx
}

func schedulerJobMessage(job scheduler.Job) string {
	return fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s\n\nDo not use the notify tool. Your final response will be delivered automatically.", job.Name, job.Message)
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
