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
	"github.com/vaayne/anna/internal/agent/selfimprove"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/scheduler"
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
	listFn := func() []channel.ModelOption {
		return collectModelsFromStore(ctx, s.store, s.snap)
	}
	switchFn := modelSwitcher(s.snap, s.store, s.pool, s.extraTools)
	return runServer(s.ctx, s, listFn, switchFn, c.Int("admin-port"), c.Bool("open"))
}

func runServer(ctx context.Context, s *setupResult, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, adminPort int, openBrowser bool) error {
	g, gctx := errgroup.WithContext(ctx)
	var channels []channel.Channel

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
	adminSrv := admin.New(s.store, as, engine, s.mem, s.db, linkCodes)

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

	catalog, err := agenttool.LoadCatalog()
	if err != nil {
		return fmt.Errorf("load channel catalog: %w", err)
	}

	// Load enabled channel plugins from settings_plugins.
	channelPlugins, err := s.store.ListPluginsByKind(gctx, config.PluginKindChannel)
	if err != nil {
		return fmt.Errorf("list channel plugins: %w", err)
	}

	for _, p := range channelPlugins {
		if !p.Enabled {
			continue
		}
		// Check if the channel has valid credentials by loading its typed config.
		if !channel.HasValidConfig(s.store, p.Name) {
			slog.Debug("skipping channel plugin without valid config", "plugin", p.ID)
			continue
		}

		def, err := resolveChannelPluginDefinition(catalog, p.Name, s.snap.Workspace, config.AnnaHome())
		if err != nil {
			return err
		}
		slog.Info("starting channel plugin", "channel", p.Name, "plugin", def.ID())
		ch := newChannelPlugin(p.Name, def)
		channels = append(channels, ch)
		adminSrv.RegisterChannelStop(p.Name, ch.Stop)
		if channel.IsNotifyEnabled(s.store, p.Name) {
			s.notifier.Register(ch)
		}
	}

	if len(channels) == 0 {
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

	// Start self-improvement review loop.
	if s.snap.SelfImprove.IsEnabled() {
		go selfimprove.StartReviewLoop(gctx, s.snap.SelfImprove, selfimprove.ReviewDeps{
			DB:        s.db,
			Store:     s.store,
			Notifier:  s.notifier,
			Workspace: s.snap.Workspace,
			Log:       slog.Default(),
		})
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
