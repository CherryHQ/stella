package discord

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type RuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(pkgchannel.DiscordConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		panic(fmt.Sprintf("discord: missing %s runtime channel factory", pkgchannel.PlatformDiscord))
	}
	if deps.Parent == nil {
		deps.Parent = context.Background()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.DiscordConfig]{
		Parent: deps.Parent, Handler: deps.Handler, Notifier: deps.Notifications, Log: deps.Log,
		Platform: pkgchannel.PlatformDiscord, DecodeConfig: DecodeConfig,
		ConfigureConfig: func(cfg pkgchannel.DiscordConfig, desired pkgplugins.PluginState) pkgchannel.DiscordConfig {
			if cfg.InstanceID == "" {
				cfg.InstanceID = desired.ID
			}
			return cfg
		},
		ValidateConfig: validateConfig, NewChannel: deps.NewChannel, Now: deps.Now,
		WrapHandler: internalchannel.WrapOperationHandler,
		Snapshot: func(now time.Time, state pkgplugins.RuntimeState, message string, _ pkgchannel.DiscordConfig) pkgplugins.RuntimeStatus {
			return pkgplugins.RuntimeStatus{State: state, Message: message, UpdatedAt: now, Metadata: map[string]any{}}
		},
	})
}
