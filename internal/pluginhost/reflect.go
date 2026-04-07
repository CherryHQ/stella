package pluginhost

import (
	"context"
	"database/sql"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	internalreflect "github.com/vaayne/anna/internal/reflect"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type ReflectDeps struct {
	Parent    context.Context
	DB        *sql.DB
	Memory    memory.Provider
	Store     config.Store
	Notifier  *channel.Dispatcher
	Workspace string
}

func (h *Host) RegisterReflect(deps ReflectDeps) {
	h.RegisterPluginID(internalreflect.PluginID)
	h.RegisterConfig(pkgplugins.ConfigRegistration{
		PluginID:      internalreflect.PluginID,
		DefaultConfig: internalreflect.DefaultPluginConfig,
		Validate: func(raw map[string]any) error {
			_, err := internalreflect.DecodePluginConfig(raw)
			return err
		},
		Redact: internalreflect.RedactPluginConfig,
	})
	h.RegisterRuntime(pkgplugins.RuntimeRegistration{
		PluginID: internalreflect.PluginID,
		Name:     internalreflect.RuntimeName,
		Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return internalreflect.NewManagedRuntime(internalreflect.RuntimeDeps{
				Parent:    deps.Parent,
				DB:        deps.DB,
				Memory:    deps.Memory,
				Store:     deps.Store,
				Notifier:  deps.Notifier,
				Workspace: deps.Workspace,
				Log:       h.Logger(internalreflect.PluginID),
			}), nil
		},
	})
	h.RegisterStatus(pkgplugins.StatusRegistration{
		PluginID: internalreflect.PluginID,
		Get: func(ctx context.Context) (any, error) {
			snap, err := h.runtimes.Snapshot(ctx, internalreflect.PluginID, internalreflect.RuntimeName)
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
