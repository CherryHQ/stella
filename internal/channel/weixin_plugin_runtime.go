package channel

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgchannelruntime "github.com/vaayne/anna/pkg/channelruntime"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	weixinplugin "github.com/vaayne/anna/plugins/channels/weixin"
)

const (
	WeixinPluginID    = "channel/weixin"
	WeixinRuntimeName = "bot"
)

type WeixinRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   pkgplugins.NotificationRegistry
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(WeixinConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewWeixinManagedRuntime(deps WeixinRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return weixinplugin.New(weixinplugin.Config{
				BotToken: cfg.BotToken,
				BaseURL:  cfg.BaseURL,
				BotID:    cfg.BotID,
				UserID:   cfg.UserID,
			}, handler)
		}
	}
	return pkgchannelruntime.NewBotManagedRuntime(pkgchannelruntime.BotRuntimeDeps[WeixinConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifier,
		Log:                  deps.Log,
		Now:                  deps.Now,
		Platform:             PlatformWeixin,
		DecodeConfig:         DecodeWeixinPluginConfig,
		ValidateConfig:       validateWeixinConfig,
		NotificationsEnabled: func(cfg WeixinConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             weixinRuntimeSnapshot,
	})
}

func DecodeWeixinPluginConfig(raw map[string]any) (WeixinConfig, error) {
	return pkgchannel.DecodePluginConfig[WeixinConfig](raw, "weixin")
}

func RedactWeixinPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeWeixinPluginConfig(raw)
	if err != nil {
		return pkgchannel.CloneConfigMap(raw)
	}
	out := pkgchannel.CloneConfigMap(raw)
	if cfg.BotToken != "" {
		out["bot_token"] = "***"
	}
	return out
}

func validateWeixinConfig(cfg WeixinConfig) string {
	if cfg.BotToken == "" {
		return "weixin: missing bot_token"
	}
	return ""
}

func weixinRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg WeixinConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"base_url":          cfg.BaseURL,
			"bot_id":            cfg.BotID,
			"user_id":           cfg.UserID,
			"notify_enabled":    cfg.EnableNotify,
			"has_bot_identity":  cfg.BotID != "",
			"has_user_identity": cfg.UserID != "",
		},
	}
}
