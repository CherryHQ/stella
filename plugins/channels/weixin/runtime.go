package weixin

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type WeixinRuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.NotificationRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(pkgchannel.WeixinConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewWeixinManagedRuntime(deps WeixinRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg pkgchannel.WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				BotToken: cfg.BotToken,
				BaseURL:  cfg.BaseURL,
				BotID:    cfg.BotID,
				UserID:   cfg.UserID,
			}, handler)
		}
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.WeixinConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifications,
		Log:                  deps.Log,
		Now:                  deps.Now,
		Platform:             pkgchannel.PlatformWeixin,
		DecodeConfig:         DecodeConfig,
		ValidateConfig:       validateConfig,
		NotificationsEnabled: func(cfg pkgchannel.WeixinConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             runtimeSnapshot,
	})
}

func DecodeConfig(raw map[string]any) (pkgchannel.WeixinConfig, error) {
	return pkgchannel.DecodePluginConfig[pkgchannel.WeixinConfig](raw, "weixin")
}

func RedactConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return pkgchannel.CloneConfigMap(raw)
	}
	out := pkgchannel.CloneConfigMap(raw)
	if cfg.BotToken != "" {
		out["bot_token"] = "***"
	}
	return out
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"bot_token": map[string]any{
				"type":        "string",
				"description": "Weixin iLink bot token.",
			},
			"base_url": map[string]any{
				"type":        "string",
				"description": "Optional Weixin iLink base URL override.",
			},
			"bot_id": map[string]any{
				"type":        "string",
				"description": "Optional Weixin bot identity.",
			},
			"user_id": map[string]any{
				"type":        "string",
				"description": "Optional Weixin user identity.",
			},
			"enable_notify": map[string]any{
				"type":        "boolean",
				"description": "Whether scheduler and system notifications are delivered to Weixin.",
				"default":     false,
			},
		},
		"required": []any{"bot_token"},
	}
}

func validateConfig(cfg pkgchannel.WeixinConfig) string {
	if cfg.BotToken == "" {
		return "weixin: missing bot_token"
	}
	return ""
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg pkgchannel.WeixinConfig) pkgplugins.RuntimeSnapshot {
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
