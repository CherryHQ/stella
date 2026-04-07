package pluginhost

import (
	"context"

	internalchannel "github.com/vaayne/anna/internal/channel"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type TelegramDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *internalchannel.Dispatcher
	NewChannel func(internalchannel.TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func (h *Host) RegisterTelegram(deps TelegramDeps) {
	h.registerManagedRuntime(managedRuntimeRegistration{
		pluginID:      internalchannel.TelegramPluginID,
		runtimeName:   internalchannel.TelegramRuntimeName,
		defaultConfig: emptyConfig,
		validate:      validateByDecode(internalchannel.DecodeTelegramPluginConfig),
		redact:        internalchannel.RedactTelegramPluginConfig,
		factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewTelegramManagedRuntime(internalchannel.TelegramRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.TelegramPluginID),
			}), nil
		},
	})
}
