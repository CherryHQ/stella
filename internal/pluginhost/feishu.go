package pluginhost

import (
	"context"

	internalchannel "github.com/vaayne/anna/internal/channel"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type FeishuDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *internalchannel.Dispatcher
	NewChannel func(internalchannel.FeishuConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func (h *Host) RegisterFeishu(deps FeishuDeps) {
	h.registerManagedRuntime(managedRuntimeRegistration{
		pluginID:    internalchannel.FeishuPluginID,
		runtimeName: internalchannel.FeishuRuntimeName,
		metadata: pkgplugins.PluginMeta{
			ID:                    internalchannel.FeishuPluginID,
			Kind:                  "channel",
			Name:                  internalchannel.PlatformFeishu,
			DisplayName:           "Feishu",
			Description:           "Feishu bot integration.",
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
		schema:        internalchannel.FeishuPluginConfigSchema(),
		validate:      validateByDecode(internalchannel.DecodeFeishuPluginConfig),
		redact:        internalchannel.RedactFeishuPluginConfig,
		factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewFeishuManagedRuntime(internalchannel.FeishuRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.FeishuPluginID),
			}), nil
		},
	})
}
