package weixin

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type WeixinRuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(WeixinConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
	WrapHandler   pkgplugins.HandlerWrapper
	Version       string
}

func NewWeixinManagedRuntime(deps WeixinRuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID: cfg.InstanceID,
				BotToken:   cfg.BotToken,
				BaseURL:    cfg.BaseURL,
				BotID:      cfg.BotID,
				UserID:     cfg.UserID,
				Version:    deps.Version,
			}, handler)
		}
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[WeixinConfig]{
		Parent:          deps.Parent,
		Handler:         deps.Handler,
		Notifier:        deps.Notifications,
		Log:             deps.Log,
		Now:             deps.Now,
		Platform:        pkgchannel.PlatformWeixin,
		DecodeConfig:    DecodeConfig,
		ConfigureConfig: configureConfig,
		ValidateConfig:  validateConfig,
		NewChannel:      deps.NewChannel,
		Snapshot:        runtimeSnapshot,
		WrapHandler:     deps.WrapHandler,
	})
}

func configureConfig(cfg WeixinConfig, desired pkgplugins.PluginState) WeixinConfig {
	if cfg.InstanceID == "" {
		cfg.InstanceID = desired.ID
	}
	return cfg
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
		},
		"required": []any{"bot_token"},
	}
}

func validateConfig(cfg WeixinConfig) string {
	if cfg.BotToken == "" {
		return "weixin: missing bot_token"
	}
	return ""
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg WeixinConfig) pkgplugins.RuntimeStatus {
	return pkgplugins.RuntimeStatus{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"base_url":          cfg.BaseURL,
			"bot_id":            cfg.BotID,
			"user_id":           cfg.UserID,
			"has_bot_identity":  cfg.BotID != "",
			"has_user_identity": cfg.UserID != "",
		},
	}
}
