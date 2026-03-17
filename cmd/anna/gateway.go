package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/channel/feishu"
	"github.com/vaayne/anna/internal/channel/qq"
	"github.com/vaayne/anna/internal/channel/telegram"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/scheduler"
	"golang.org/x/sync/errgroup"
)

func gatewayCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "gateway",
		Usage: "Start daemon services (Telegram, etc.) based on config",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "admin-port",
				Usage: "Port for admin panel (0 = disabled)",
				Value: 0,
			},
		},
		Action: func(c *ucli.Context) error {
			ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			s, err := setup(ctx, true)
			if err != nil {
				return err
			}
			defer func() { _ = s.poolManager.Close() }()
			defer func() { _ = s.pluginMgr.Close() }()

			listFn := func() []channel.ModelOption {
				return collectModelsFromStore(ctx, s.store, s.snap)
			}
			switchFn := modelSwitcher(s.snap, s.store, s.pool, s.extraTools, s.pluginMgr.Registry())
			return runGateway(s.ctx, s, listFn, switchFn, c.Int("admin-port"))
		},
	}
}

func runGateway(ctx context.Context, s *setupResult, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, adminPort int) error {
	g, gctx := errgroup.WithContext(ctx)
	var channels []channel.Channel

	// Optionally start admin panel server.
	if adminPort > 0 {
		adminSrv := admin.New(s.store, s.mem, s.db)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", adminPort))
		if err != nil {
			return fmt.Errorf("admin listen: %w", err)
		}
		slog.Info("starting admin panel", "addr", ln.Addr().String())
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

	// Load channel configs from DB.
	tgCfg := loadChannelConfig[telegramChannelConfig](s.store, "telegram")
	qqCfg := loadChannelConfig[qqChannelConfig](s.store, "qq")
	fsCfg := loadChannelConfig[feishuChannelConfig](s.store, "feishu")

	// --- Telegram ---
	if tgCfg != nil && tgCfg.Token != "" {
		slog.Info("starting telegram bot")

		tgBot, err := telegram.New(telegram.Config{
			Token:      tgCfg.Token,
			NotifyChat: tgCfg.NotifyChat,
			ChannelID:  tgCfg.ChannelID,
			GroupMode:  tgCfg.GroupMode,
			AllowedIDs: tgCfg.AllowedIDs,
		}, s.pool, listFn, switchFn,
			telegram.WithPoolManager(s.poolManager),
			telegram.WithStore(s.store),
		)
		if err != nil {
			return fmt.Errorf("create telegram bot: %w", err)
		}

		defaultChat := tgCfg.NotifyChat
		if defaultChat == "" {
			defaultChat = tgCfg.ChannelID
		}
		channels = append(channels, tgBot)
		if tgCfg.EnableNotify {
			s.notifier.Register(tgBot, defaultChat)
		}
	}

	// --- QQ ---
	if qqCfg != nil && qqCfg.AppID != "" && qqCfg.AppSecret != "" {
		slog.Info("starting qq bot")

		qqBot, err := qq.New(qq.Config{
			AppID:      qqCfg.AppID,
			AppSecret:  qqCfg.AppSecret,
			GroupMode:  qqCfg.GroupMode,
			AllowedIDs: qqCfg.AllowedIDs,
		}, s.pool, listFn, switchFn,
			qq.WithPoolManager(s.poolManager),
			qq.WithStore(s.store),
		)
		if err != nil {
			return fmt.Errorf("create qq bot: %w", err)
		}

		channels = append(channels, qqBot)
		if qqCfg.EnableNotify {
			s.notifier.Register(qqBot, "")
		}
	}

	// --- Feishu ---
	if fsCfg != nil && fsCfg.AppID != "" && fsCfg.AppSecret != "" {
		slog.Info("starting feishu bot")

		fsBot, err := feishu.New(feishu.Config{
			AppID:             fsCfg.AppID,
			AppSecret:         fsCfg.AppSecret,
			EncryptKey:        fsCfg.EncryptKey,
			VerificationToken: fsCfg.VerificationToken,
			NotifyChat:        fsCfg.NotifyChat,
			GroupMode:         fsCfg.GroupMode,
			AllowedIDs:        fsCfg.AllowedIDs,
		}, s.pool, listFn, switchFn,
			feishu.WithPoolManager(s.poolManager),
			feishu.WithStore(s.store),
		)
		if err != nil {
			return fmt.Errorf("create feishu bot: %w", err)
		}

		channels = append(channels, fsBot)
		if fsCfg.EnableNotify {
			s.notifier.Register(fsBot, fsCfg.NotifyChat)
		}
	}

	if len(channels) == 0 {
		return fmt.Errorf("no gateway services configured. Run 'anna onboard' to set up channels")
	}

	if len(s.notifier.Channels()) == 0 {
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

	err := g.Wait()
	slog.Info("gateway stopped")
	return err
}

// wireSchedulerNotifier overrides the scheduler callback to collect the agent response
// and broadcast it via the notification dispatcher. When a job has an AgentID, the
// corresponding pool is looked up via the PoolManager; otherwise the default pool is used.
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
			if err := dispatcher.Notify(ctx, channel.Notification{Text: text}); err != nil {
				slog.Error("scheduler notification failed", "job_id", job.ID, "error", err)
			}
		}
	})
}

// --- Channel config types for JSON deserialization ---

type telegramChannelConfig struct {
	Token        string  `json:"token"`
	NotifyChat   string  `json:"notify_chat"`
	ChannelID    string  `json:"channel_id"`
	GroupMode    string  `json:"group_mode"`
	AllowedIDs   []int64 `json:"allowed_ids"`
	EnableNotify bool    `json:"enable_notify"`
}

type qqChannelConfig struct {
	AppID        string   `json:"app_id"`
	AppSecret    string   `json:"app_secret"`
	GroupMode    string   `json:"group_mode"`
	AllowedIDs   []string `json:"allowed_ids"`
	EnableNotify bool     `json:"enable_notify"`
}

type feishuChannelConfig struct {
	AppID             string   `json:"app_id"`
	AppSecret         string   `json:"app_secret"`
	EncryptKey        string   `json:"encrypt_key"`
	VerificationToken string   `json:"verification_token"`
	NotifyChat        string   `json:"notify_chat"`
	GroupMode         string   `json:"group_mode"`
	AllowedIDs        []string `json:"allowed_ids"`
	EnableNotify      bool     `json:"enable_notify"`
}

// loadChannelConfig loads a channel's JSON config from the store and
// deserializes it into the given type. Returns nil if not found.
func loadChannelConfig[T any](store config.Store, channelID string) *T {
	ch, err := store.GetChannel(context.Background(), channelID)
	if err != nil {
		return nil
	}
	var cfg T
	if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
		slog.Warn("failed to parse channel config", "channel", channelID, "error", err)
		return nil
	}
	return &cfg
}
