package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/pluginhost"
	internalreflect "github.com/vaayne/anna/internal/reflect"
	"github.com/vaayne/anna/internal/scheduler"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
	"golang.org/x/sync/errgroup"
)

const defaultAdminPort = 25678

func serverFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.IntFlag{
			Name:    "admin-port",
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
	listFn := func() []pkgchannel.ModelOption {
		return collectModelsFromStore(ctx, s.store, s.snap)
	}
	switchFn := modelSwitcher(
		s.snap,
		s.store,
		s.pool,
		s.extraTools,
		func(bc plugintools.BuildContext) []tools.Tool {
			return s.pluginHost.BuildCoreTools(bc)
		},
		func(api, apiKey, baseURL string) (*providers.Registry, error) {
			return s.pluginHost.BuildProviderRegistry(api, map[string]any{
				"api_key":  apiKey,
				"base_url": baseURL,
			})
		},
	)
	return runServer(s.ctx, s, listFn, switchFn, c.Int("admin-port"), c.Bool("open"))
}

func runServer(ctx context.Context, s *setupResult, listFn func() []pkgchannel.ModelOption, switchFn func(string, string) error, adminPort int, openBrowser bool) error {
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

	// Create the coordinator that implements MessageHandler for all channels.
	coordinator := channel.NewCoordinator(
		s.poolManager,
		s.store,
		listFn,
		switchFn,
		channel.WithCoordinatorAuth(as, engine, linkCodes),
	)
	if s.channelRuntimeServices != nil {
		s.channelRuntimeServices.Set(gctx, coordinator, s.notifier)
	}

	hostBackedRegistrations := map[string]func(){
		channel.QQPluginID: func() {
			s.pluginHost.RegisterQQ(pluginhost.QQDeps{
				Parent:   gctx,
				Handler:  coordinator,
				Notifier: s.notifier,
			})
		},
		channel.FeishuPluginID: func() {
			s.pluginHost.RegisterFeishu(pluginhost.FeishuDeps{
				Parent:   gctx,
				Handler:  coordinator,
				Notifier: s.notifier,
			})
		},
		channel.WeixinPluginID: func() {
			s.pluginHost.RegisterWeixin(pluginhost.WeixinDeps{
				Parent:   gctx,
				Handler:  coordinator,
				Notifier: s.notifier,
			})
		},
	}
	if s.pluginHost != nil {
		for _, hostBackedChannel := range []channel.HostBackedChannel{
			{Name: channel.PlatformQQ, PluginID: channel.QQPluginID},
			{Name: channel.PlatformFeishu, PluginID: channel.FeishuPluginID},
			{Name: channel.PlatformWeixin, PluginID: channel.WeixinPluginID},
		} {
			hostBackedRegistrations[hostBackedChannel.PluginID]()
			if err := s.pluginHost.ApplyPlugin(gctx, hostBackedChannel.PluginID); err != nil {
				return fmt.Errorf("apply %s runtime: %w", hostBackedChannel.Name, err)
			}
		}
		if err := s.pluginHost.ApplyPlugin(gctx, channel.TelegramPluginID); err != nil {
			return fmt.Errorf("apply %s runtime: %w", channel.PlatformTelegram, err)
		}
	}

	// Start admin panel server.
	if adminPort > 0 {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", adminPort))
		if err != nil {
			return fmt.Errorf("admin listen: %w", err)
		}
		addr := ln.Addr().String()
		slog.Info("starting admin panel", "addr", addr)
		fmt.Printf("Admin panel running at http://%s\n", addr)

		if openBrowser {
			launchBrowser("http://" + addr)
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

	// Configure channel hot-reload so the admin UI can start/stop channels.
	adminSrv.SetChannelLifecycle(gctx,
		func(name string) (pkgchannel.Channel, error) {
			if !channel.HasValidConfig(s.store, name) {
				return nil, fmt.Errorf("%s: missing or invalid config", name)
			}
			return buildChannel(name, coordinator, s.store)
		},
		func(name string, ch pkgchannel.Channel) {
			if channel.IsNotifyEnabled(s.store, name) {
				s.notifier.Register(ch)
			}
		},
		func(name string) {
			s.notifier.Unregister(name)
		},
	)

	hostBackedConfigured := false
	for _, hostBackedChannel := range channel.HostBackedChannels {
		if p, err := s.store.GetPlugin(gctx, hostBackedChannel.PluginID); err == nil && p.Enabled && channel.HasValidConfig(s.store, hostBackedChannel.Name) {
			hostBackedConfigured = true
			break
		}
	}

	if len(channels) == 0 && !hostBackedConfigured {
		if adminPort > 0 {
			slog.Warn("no channel services configured; running admin panel only")
		} else {
			return fmt.Errorf("no services to run: no channels configured and admin panel disabled")
		}
	}

	if len(channels) > 0 && len(s.notifier.Channels()) == 0 {
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

	// Wire auth store into dispatcher for per-user notification routing.
	s.notifier.SetAuthStore(as)

	// Wire scheduler notifications and start the scheduler AFTER channels
	// are registered, so early-firing jobs already use the dispatcher.
	if s.schedulerSvc != nil {
		if s.snap.Scheduler.IsEnabled() {
			wireSchedulerNotifier(s.schedulerSvc, s.poolManager, s.pool, s.notifier)
			if err := s.schedulerSvc.Start(ctx); err != nil {
				return fmt.Errorf("start scheduler: %w", err)
			}
		} else {
			if err := s.schedulerSvc.StartEphemeral(ctx); err != nil {
				return fmt.Errorf("start shared scheduler: %w", err)
			}
		}
		defer func() { _ = s.schedulerSvc.Stop() }()
	}

	if s.snap.Heartbeat.IsEnabled() && s.schedulerSvc != nil {
		if err := s.schedulerSvc.StartHeartbeat(ctx, s.snap.Heartbeat.Interval()); err != nil {
			return fmt.Errorf("schedule heartbeat: %w", err)
		}
	}

	if err := s.pluginHost.ApplyPlugin(gctx, internalreflect.PluginID); err != nil {
		return fmt.Errorf("apply reflect runtime: %w", err)
	}

	waitErr := g.Wait()
	slog.Info("gateway stopped")
	return waitErr
}

// wireSchedulerNotifier overrides the scheduler callback to collect the agent response
// and dispatch it via the notification dispatcher. User-owned jobs notify only their
// owner; system jobs (user_id=0) broadcast to all channels.
func wireSchedulerNotifier(schedulerSvc *scheduler.Service, poolMgr *agent.PoolManager, defaultPool *agent.Pool, dispatcher *channel.Dispatcher) {
	schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) {
		pool := defaultPool
		if job.AgentID != "" {
			if p := poolMgr.Get(job.AgentID); p != nil {
				pool = p
			} else {
				slog.Warn("scheduler job references unknown agent, using default pool",
					"job_id", job.ID, "agent_id", job.AgentID)
			}
		}
		sessionID := job.SessionID()
		msg := fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s", job.Name, job.Message)
		var result strings.Builder
		for evt := range pool.Chat(ctx, sessionID, msg) {
			if evt.Err != nil {
				slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
			}
			if evt.Text != "" {
				result.WriteString(evt.Text)
			}
		}
		if result.Len() > 0 {
			text := fmt.Sprintf("*%s*\n\n%s", job.Name, result.String())
			n := channel.Notification{Text: text}
			var err error
			if job.UserID != 0 {
				// User-owned job: notify only the owner.
				err = dispatcher.NotifyUser(ctx, job.UserID, n)
			} else {
				// System job: broadcast to all channels.
				err = dispatcher.Notify(ctx, n)
			}
			if err != nil {
				slog.Error("scheduler notification failed", "job_id", job.ID, "error", err)
			}
		}
	})
}

func launchBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		if err := cmd.Start(); err != nil {
			slog.Warn("failed to open browser", "error", err)
			return
		}
		go func() { _ = cmd.Wait() }()
	}
}
