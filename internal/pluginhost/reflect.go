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
	h.registerManagedRuntime(managedRuntimeRegistration{
		pluginID:      internalreflect.PluginID,
		runtimeName:   internalreflect.RuntimeName,
		defaultConfig: internalreflect.DefaultPluginConfig,
		validate:      validateByDecode(internalreflect.DecodePluginConfig),
		redact:        internalreflect.RedactPluginConfig,
		factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
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
}
