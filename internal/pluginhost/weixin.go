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
	h.RegisterPluginID(internalchannel.WeixinPluginID)
	h.RegisterConfig(pkgplugins.ConfigRegistration{
		PluginID:      internalchannel.WeixinPluginID,
		DefaultConfig: func() map[string]any { return map[string]any{} },
		Validate: func(raw map[string]any) error {
			_, err := internalchannel.DecodeWeixinPluginConfig(raw)
			return err
		},
		Redact: internalchannel.RedactWeixinPluginConfig,
	})
	h.RegisterRuntime(pkgplugins.RuntimeRegistration{
		PluginID: internalchannel.WeixinPluginID,
		Name:     internalchannel.WeixinRuntimeName,
		Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewWeixinManagedRuntime(internalchannel.WeixinRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.WeixinPluginID),
			}), nil
		},
	})
	h.RegisterStatus(pkgplugins.StatusRegistration{
		PluginID: internalchannel.WeixinPluginID,
		Get: func(ctx context.Context) (any, error) {
			snap, err := h.runtimes.Snapshot(ctx, internalchannel.WeixinPluginID, internalchannel.WeixinRuntimeName)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"state":      snap.State,
				"message":    snap.Message,
				"updated_at": snap.UpdatedAt,
				"metadata":   snap.Metadata,
			}, nil
		},
	})
}
