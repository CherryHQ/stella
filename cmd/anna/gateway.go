package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"strings"
	"syscall"

	ucli "github.com/urfave/cli/v2"
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
		Action: func(c *ucli.Context) error {
			ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			s, err := setup(ctx, true)
			if err != nil {
				return err
			}
			defer func() { _ = s.pool.Close() }()
			defer func() { _ = s.pluginMgr.Close() }()

			// Scheduler is started inside runGateway after notification wiring,
			// so early-firing jobs already have the dispatcher callback.

			listFn := func() []channel.ModelOption { return collectModels(s.cfg) }
			switchFn := modelSwitcher(s.cfg, s.pool, s.extraTools, s.pluginMgr.Registry())
			return runGateway(s.ctx, s, listFn, switchFn)
		},
	}
}

func runGateway(ctx context.Context, s *setupResult, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc) error {
	g, gctx := errgroup.WithContext(ctx)
	var channels []channel.Channel

	// --- Telegram ---
	tg := s.cfg.Channels.Telegram
	if tg.IsEnabled() && tg.Token != "" {
		slog.Info("starting telegram bot")

		tgBot, err := telegram.New(telegram.Config{
			Token:      tg.Token,
			NotifyChat: tg.NotifyChat,
			ChannelID:  tg.ChannelID,
			GroupMode:  tg.GroupMode,
			AllowedIDs: tg.AllowedIDs,
		}, s.pool, listFn, switchFn)
		if err != nil {
			return fmt.Errorf("create telegram bot: %w", err)
		}

		defaultChat := tg.NotifyChat
		if defaultChat == "" {
			defaultChat = tg.ChannelID
		}
		channels = append(channels, tgBot)
		if tg.IsEnabled() && tg.IsNotifyEnabled() {
			s.notifier.Register(tgBot, defaultChat)
		}
	}

	// --- QQ ---
	qqCfg := s.cfg.Channels.QQ
	if qqCfg.IsEnabled() && qqCfg.AppID != "" && qqCfg.AppSecret != "" {
		slog.Info("starting qq bot")

		qqBot, err := qq.New(qq.Config{
			AppID:      qqCfg.AppID,
			AppSecret:  qqCfg.AppSecret,
			GroupMode:  qqCfg.GroupMode,
			AllowedIDs: qqCfg.AllowedIDs,
		}, s.pool, listFn, switchFn)
		if err != nil {
			return fmt.Errorf("create qq bot: %w", err)
		}

		channels = append(channels, qqBot)
		if qqCfg.IsEnabled() && qqCfg.IsNotifyEnabled() {
			s.notifier.Register(qqBot, "")
		}
	}

	// --- Feishu ---
	fsCfg := s.cfg.Channels.Feishu
	if fsCfg.IsEnabled() && fsCfg.AppID != "" && fsCfg.AppSecret != "" {
		slog.Info("starting feishu bot")

		fsBot, err := feishu.New(feishu.Config{
			AppID:             fsCfg.AppID,
			AppSecret:         fsCfg.AppSecret,
			EncryptKey:        fsCfg.EncryptKey,
			VerificationToken: fsCfg.VerificationToken,
			NotifyChat:        fsCfg.NotifyChat,
			GroupMode:         fsCfg.GroupMode,
			AllowedIDs:        fsCfg.AllowedIDs,
		}, s.pool, listFn, switchFn)
		if err != nil {
			return fmt.Errorf("create feishu bot: %w", err)
		}

		channels = append(channels, fsBot)
		if fsCfg.IsEnabled() && fsCfg.IsNotifyEnabled() {
			s.notifier.Register(fsBot, fsCfg.NotifyChat)
		}
	}

	if len(channels) == 0 {
		return fmt.Errorf("no gateway services configured. Check %s", config.Path())
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
		if s.cfg.Scheduler.IsEnabled() {
			wireSchedulerNotifier(s.schedulerSvc, s.pool, s.notifier)
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

	if s.cfg.Heartbeat.IsEnabled() && s.schedulerSvc != nil {
		if err := s.schedulerSvc.StartHeartbeat(ctx, s.cfg.Heartbeat.Interval()); err != nil {
			return fmt.Errorf("schedule heartbeat: %w", err)
		}
	}

	err := g.Wait()
	slog.Info("gateway stopped")
	return err
}

// wireSchedulerNotifier overrides the scheduler callback to collect the agent response
// and broadcast it via the notification dispatcher.
func wireSchedulerNotifier(schedulerSvc *scheduler.Service, pool *agent.Pool, dispatcher *channel.Dispatcher) {
	schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) {
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
