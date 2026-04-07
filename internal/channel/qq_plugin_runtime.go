package channel

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	qqplugin "github.com/vaayne/anna/plugins/channels/qq"
)

const (
	QQPluginID    = "channel/qq"
	QQRuntimeName = "bot"
)

type QQRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *Dispatcher
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(QQConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

type qqManagedRuntime = botManagedRuntime[QQConfig]

func NewQQManagedRuntime(deps QQRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg QQConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return qqplugin.New(qqplugin.Config{
				AppID:     cfg.AppID,
				AppSecret: cfg.AppSecret,
				GroupMode: cfg.GroupMode,
			}, handler)
		}
	}
	return newBotManagedRuntime(botRuntimeDeps[QQConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifier,
		Log:                  deps.Log,
		Now:                  deps.Now,
		Platform:             PlatformQQ,
		DecodeConfig:         DecodeQQPluginConfig,
		ValidateConfig:       validateQQConfig,
		NotificationsEnabled: func(cfg QQConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             qqRuntimeSnapshot,
	})
}

func DecodeQQPluginConfig(raw map[string]any) (QQConfig, error) {
	return decodePluginConfig[QQConfig](raw, "qq")
}

func RedactQQPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeQQPluginConfig(raw)
	if err != nil {
		return cloneConfigMap(raw)
	}
	out := cloneConfigMap(raw)
	if cfg.AppSecret != "" {
		out["app_secret"] = "***"
	}
	return out
}

func validateQQConfig(cfg QQConfig) string {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return "qq: missing app_id or app_secret"
	}
	return ""
}

func qqRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg QQConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"app_id":         cfg.AppID,
			"group_mode":     cfg.GroupMode,
			"notify_enabled": cfg.EnableNotify,
		},
	}
}
