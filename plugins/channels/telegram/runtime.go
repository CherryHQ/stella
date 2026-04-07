package telegram

import (
	"context"
	"fmt"
	"time"

	internalchannel "github.com/vaayne/anna/internal/channel"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type RuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *internalchannel.Dispatcher
	Now        func() time.Time
	NewChannel func(internalchannel.TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		panic(fmt.Sprintf("telegram: missing %s runtime channel factory", internalchannel.PlatformTelegram))
	}
	return newBotManagedRuntime(botRuntimeDeps(deps))
}

type botRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *internalchannel.Dispatcher
	Now        func() time.Time
	NewChannel func(internalchannel.TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func newBotManagedRuntime(deps botRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.Parent == nil {
		deps.Parent = context.Background()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	return internalchannel.NewBotManagedRuntime(internalchannel.BotRuntimeDeps[internalchannel.TelegramConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifier,
		Platform:             internalchannel.PlatformTelegram,
		DecodeConfig:         DecodeConfig,
		ValidateConfig:       validateConfig,
		NotificationsEnabled: func(cfg internalchannel.TelegramConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             runtimeSnapshot,
		Now:                  deps.Now,
	})
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg internalchannel.TelegramConfig) pkgplugins.RuntimeSnapshot {
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
