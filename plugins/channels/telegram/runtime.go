package telegram

import (
	"context"
	"fmt"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type RuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Now           func() time.Time
	NewChannel    func(pkgchannel.TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
	WrapHandler   pkgplugins.HandlerWrapper
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		panic(fmt.Sprintf("telegram: missing %s runtime channel factory", pkgchannel.PlatformTelegram))
	}
	return newBotManagedRuntime(botRuntimeDeps(deps))
}

type botRuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Now           func() time.Time
	NewChannel    func(pkgchannel.TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
	WrapHandler   pkgplugins.HandlerWrapper
}

func newBotManagedRuntime(deps botRuntimeDeps) pkgplugins.Runtime {
	if deps.Parent == nil {
		deps.Parent = context.Background()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.TelegramConfig]{
		Parent:          deps.Parent,
		Handler:         deps.Handler,
		Notifier:        deps.Notifications,
		Platform:        pkgchannel.PlatformTelegram,
		DecodeConfig:    DecodeConfig,
		ConfigureConfig: configureConfig,
		ValidateConfig:  validateConfig,
		NewChannel:      deps.NewChannel,
		Snapshot:        runtimeSnapshot,
		Now:             deps.Now,
		WrapHandler:     deps.WrapHandler,
	})
}

func configureConfig(cfg pkgchannel.TelegramConfig, desired pkgplugins.PluginState) pkgchannel.TelegramConfig {
	if cfg.InstanceID == "" {
		cfg.InstanceID = desired.ID
	}
	return cfg
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg pkgchannel.TelegramConfig) pkgplugins.RuntimeStatus {
	return pkgplugins.RuntimeStatus{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"channel_id":          cfg.ChannelID,
			"has_default_channel": cfg.ChannelID != "",
		},
	}
}
