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
	h.RegisterPluginID(internalchannel.TelegramPluginID)
	h.RegisterConfig(pkgplugins.ConfigRegistration{
		PluginID:      internalchannel.TelegramPluginID,
		DefaultConfig: func() map[string]any { return map[string]any{} },
		Validate: func(raw map[string]any) error {
			_, err := internalchannel.DecodeTelegramPluginConfig(raw)
			return err
		},
		Redact: internalchannel.RedactTelegramPluginConfig,
	})
	h.RegisterRuntime(pkgplugins.RuntimeRegistration{
		PluginID: internalchannel.TelegramPluginID,
		Name:     internalchannel.TelegramRuntimeName,
		Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalchannel.NewTelegramManagedRuntime(internalchannel.TelegramRuntimeDeps{
				Parent:     deps.Parent,
				Handler:    deps.Handler,
				Notifier:   deps.Notifier,
				NewChannel: deps.NewChannel,
				Log:        h.Logger(internalchannel.TelegramPluginID),
			}), nil
		},
	})
	h.RegisterStatus(pkgplugins.StatusRegistration{
		PluginID: internalchannel.TelegramPluginID,
		Get: func(ctx context.Context) (any, error) {
			snap, err := h.runtimes.Snapshot(ctx, internalchannel.TelegramPluginID, internalchannel.TelegramRuntimeName)
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
