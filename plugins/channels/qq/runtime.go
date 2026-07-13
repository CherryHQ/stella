package qq

import (
	"context"
	"log/slog"
	"time"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type QQRuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(pkgchannel.QQConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewQQManagedRuntime(deps QQRuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg pkgchannel.QQConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID: cfg.InstanceID,
				AppID:      cfg.AppID,
				AppSecret:  cfg.AppSecret,
			}, handler)
		}
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.QQConfig]{
		Parent:          deps.Parent,
		Handler:         deps.Handler,
		Notifier:        deps.Notifications,
		Log:             deps.Log,
		Now:             deps.Now,
		Platform:        pkgchannel.PlatformQQ,
		DecodeConfig:    DecodeConfig,
		ConfigureConfig: configureConfig,
		ValidateConfig:  validateConfig,
		NewChannel:      deps.NewChannel,
		Snapshot:        runtimeSnapshot,
		WrapHandler:     internalchannel.WrapOperationHandler,
	})
}

func configureConfig(cfg pkgchannel.QQConfig, desired pkgplugins.PluginState) pkgchannel.QQConfig {
	if cfg.InstanceID == "" {
		cfg.InstanceID = desired.ID
	}
	return cfg
}

func DecodeConfig(raw map[string]any) (pkgchannel.QQConfig, error) {
	return pkgchannel.DecodePluginConfig[pkgchannel.QQConfig](raw, "qq")
}

func RedactConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return pkgchannel.CloneConfigMap(raw)
	}
	out := pkgchannel.CloneConfigMap(raw)
	if cfg.AppSecret != "" {
		out["app_secret"] = "***"
	}
	return out
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"app_id": map[string]any{
				"type":        "string",
				"description": "QQ bot app ID.",
			},
			"app_secret": map[string]any{
				"type":        "string",
				"description": "QQ bot app secret.",
			},
		},
		"required": []any{"app_id", "app_secret"},
	}
}

func validateConfig(cfg pkgchannel.QQConfig) string {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return "qq: missing app_id or app_secret"
	}
	return ""
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg pkgchannel.QQConfig) pkgplugins.RuntimeStatus {
	return pkgplugins.RuntimeStatus{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"app_id": cfg.AppID,
		},
	}
}
