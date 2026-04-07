package pluginhost

import (
	"context"

	internalchannel "github.com/vaayne/anna/internal/channel"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type QQDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *internalchannel.Dispatcher
	NewChannel func(internalchannel.QQConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func (h *Host) RegisterQQ(deps QQDeps) {
	h.registerManagedRuntime(managedRuntimeRegistration{
		pluginID:      internalchannel.QQPluginID,
		runtimeName:   internalchannel.QQRuntimeName,
		defaultConfig: emptyConfig,
		validate:      validateByDecode(internalchannel.DecodeQQPluginConfig),
		redact:        internalchannel.RedactQQPluginConfig,
		factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewQQManagedRuntime(internalchannel.QQRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.QQPluginID),
			}), nil
		},
	})
}
