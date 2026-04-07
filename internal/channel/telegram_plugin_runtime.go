package channel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const (
	TelegramPluginID    = "channel/telegram"
	TelegramRuntimeName = "bot"
)

type TelegramRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *Dispatcher
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

type telegramManagedRuntime = botManagedRuntime[TelegramConfig]

func NewTelegramManagedRuntime(deps TelegramRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		panic(fmt.Sprintf("channel: missing %s runtime channel factory", PlatformTelegram))
	}
	return newBotManagedRuntime(botRuntimeDeps[TelegramConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifier,
		Log:                  deps.Log,
		Now:                  deps.Now,
		Platform:             PlatformTelegram,
		DecodeConfig:         DecodeTelegramPluginConfig,
		ValidateConfig:       validateTelegramConfig,
		NotificationsEnabled: func(cfg TelegramConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             telegramRuntimeSnapshot,
	})
}

func DecodeTelegramPluginConfig(raw map[string]any) (TelegramConfig, error) {
	return decodePluginConfig[TelegramConfig](raw, "telegram")
}

func RedactTelegramPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeTelegramPluginConfig(raw)
	if err != nil {
		return cloneConfigMap(raw)
	}
	out := cloneConfigMap(raw)
	if cfg.Token != "" {
		out["token"] = "***"
	}
	return out
}

func validateTelegramConfig(cfg TelegramConfig) string {
	if cfg.Token == "" {
		return "telegram: missing token"
	}
	return ""
}

func telegramRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg TelegramConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"channel_id":          cfg.ChannelID,
			"group_mode":          cfg.GroupMode,
			"notify_enabled":      cfg.EnableNotify,
			"has_default_channel": cfg.ChannelID != "",
		},
	}
}
