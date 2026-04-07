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
	h.RegisterPluginID(internalchannel.FeishuPluginID)
	h.RegisterConfig(pkgplugins.ConfigRegistration{
		PluginID:      internalchannel.FeishuPluginID,
		DefaultConfig: func() map[string]any { return map[string]any{} },
		Validate: func(raw map[string]any) error {
			_, err := internalchannel.DecodeFeishuPluginConfig(raw)
			return err
		},
		Redact: internalchannel.RedactFeishuPluginConfig,
	})
	h.RegisterRuntime(pkgplugins.RuntimeRegistration{
		PluginID: internalchannel.FeishuPluginID,
		Name:     internalchannel.FeishuRuntimeName,
		Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewFeishuManagedRuntime(internalchannel.FeishuRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.FeishuPluginID),
			}), nil
		},
	})
	h.RegisterStatus(pkgplugins.StatusRegistration{
		PluginID: internalchannel.FeishuPluginID,
		Get: func(ctx context.Context) (any, error) {
			snap, err := h.runtimes.Snapshot(ctx, internalchannel.FeishuPluginID, internalchannel.FeishuRuntimeName)
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
