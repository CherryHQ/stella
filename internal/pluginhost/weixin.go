package pluginhost

import (
	"context"

	internalchannel "github.com/vaayne/anna/internal/channel"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type WeixinDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *internalchannel.Dispatcher
	NewChannel func(internalchannel.WeixinConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func (h *Host) RegisterWeixin(deps WeixinDeps) {
	h.registerManagedRuntime(managedRuntimeRegistration{
		pluginID:    internalchannel.WeixinPluginID,
		runtimeName: internalchannel.WeixinRuntimeName,
		metadata: pkgplugins.PluginMeta{
			ID:                    internalchannel.WeixinPluginID,
			Kind:                  "channel",
			Name:                  internalchannel.PlatformWeixin,
			DisplayName:           "Weixin",
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
		schema:        internalchannel.WeixinPluginConfigSchema(),
		validate:      validateByDecode(internalchannel.DecodeWeixinPluginConfig),
		redact:        internalchannel.RedactWeixinPluginConfig,
		factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewWeixinManagedRuntime(internalchannel.WeixinRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.WeixinPluginID),
			}), nil
		},
	})
}
