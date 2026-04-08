package pluginhost

import (
	"context"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func emptyConfig() map[string]any {
	return map[string]any{}
}

func validateByDecode[T any](decode func(map[string]any) (T, error)) func(map[string]any) error {
	return func(raw map[string]any) error {
		_, err := decode(raw)
		return err
	}
}

func (h *Host) registerManagedRuntime(reg managedRuntimeRegistration) {
	h.RegisterPluginID(reg.pluginID)
	if reg.metadata.ID != "" {
		meta := reg.metadata.Clone()
		meta.Managed = true
		meta.HasConfig = true
		meta.HasStatus = true
		h.RegisterMetadata(meta)
	}
	h.RegisterConfig(pkgplugins.ConfigRegistration{
		PluginID:      reg.pluginID,
		DefaultConfig: reg.defaultConfig,
		Schema:        reg.schema,
		Validate:      reg.validate,
		Redact:        reg.redact,
	})
	h.RegisterRuntime(pkgplugins.RuntimeRegistration{
		PluginID: reg.pluginID,
		Name:     reg.runtimeName,
		Factory:  reg.factory,
	})
	h.RegisterStatus(pkgplugins.StatusRegistration{
		PluginID: reg.pluginID,
		Get: func(ctx context.Context) (any, error) {
			return h.runtimeStatus(ctx, reg.pluginID, reg.runtimeName)
		},
	})
}

func (h *Host) runtimeStatus(ctx context.Context, pluginID, runtimeName string) (any, error) {
	snap, err := h.runtimes.Snapshot(ctx, pluginID, runtimeName)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"state":      snap.State,
		"message":    snap.Message,
		"updated_at": snap.UpdatedAt,
		"metadata":   snap.Metadata,
	}, nil
}

type managedRuntimeRegistration struct {
	pluginID      string
	runtimeName   string
	metadata      pkgplugins.PluginMeta
	defaultConfig func() map[string]any
	schema        map[string]any
	validate      func(map[string]any) error
	redact        func(map[string]any) map[string]any
	factory       func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error)
}
