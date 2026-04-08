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
		pluginID:    internalchannel.QQPluginID,
		runtimeName: internalchannel.QQRuntimeName,
		metadata: pkgplugins.PluginMeta{
			ID:                    internalchannel.QQPluginID,
			Kind:                  "channel",
			Name:                  internalchannel.PlatformQQ,
			DisplayName:           "QQ",
			Description:           "QQ bot integration.",
			AdminVisible:          true,
			SupportsNotifications: true,
			Capabilities: []string{
				pkgplugins.CapabilityChannel,
				pkgplugins.CapabilityRuntime,
				pkgplugins.CapabilityConfig,
				pkgplugins.CapabilityStatus,
			},
		},
		defaultConfig: emptyConfig,
		schema:        internalchannel.QQPluginConfigSchema(),
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
