package plugins

import "context"

type ManagedChannelPluginRegistration struct {
	PluginID             string
	RuntimeName          string
	Meta                 PluginMeta
	DefaultConfig        func() map[string]any
	Schema               map[string]any
	Validate             func(raw map[string]any) error
	Redact               func(raw map[string]any) map[string]any
	Configured           func(raw map[string]any) bool
	NotificationsEnabled func(raw map[string]any) bool
	RuntimeFactory       func(ServiceHost) (ManagedRuntime, error)
}

func RegisterManagedChannelPlugin(host Host, reg ManagedChannelPluginRegistration) {
	meta := reg.Meta.Clone()
	if meta.ID == "" {
		meta.ID = reg.PluginID
	}
	if meta.Kind == "" {
		meta.Kind = "channel"
	}
	meta.Managed = true
	meta.HasConfig = true
	meta.HasStatus = true

	host.Registry().RegisterMetadata(meta)
	host.Registry().RegisterConfig(ConfigRegistration{
		PluginID:      reg.PluginID,
		DefaultConfig: reg.DefaultConfig,
		Schema:        reg.Schema,
		Validate:      reg.Validate,
		Redact:        reg.Redact,
	})
	host.Registry().RegisterChannel(ChannelRegistration{
		PluginID:              reg.PluginID,
		Name:                  meta.Name,
		SupportsNotifications: meta.SupportsNotifications,
		Configured:            reg.Configured,
		NotificationsEnabled:  reg.NotificationsEnabled,
	})
	host.Registry().RegisterRuntime(RuntimeRegistration{
		PluginID: reg.PluginID,
		Name:     reg.RuntimeName,
		Factory: func(ctx RuntimeContext) (ManagedRuntime, error) {
			return reg.RuntimeFactory(ctx.Services)
		},
	})
	host.Registry().RegisterStatus(StatusRegistration{
		PluginID: reg.PluginID,
		Get: func(ctx context.Context) (any, error) {
			return managedRuntimeStatus(ctx, host.Services().Runtime(), reg.PluginID, reg.RuntimeName)
		},
	})
}

func managedRuntimeStatus(ctx context.Context, runtime RuntimeLookup, pluginID, runtimeName string) (any, error) {
	handle, ok := runtime.Get(pluginID, runtimeName)
	if !ok {
		return map[string]any{
			"state":      RuntimeStateStopped,
			"updated_at": nil,
			"metadata":   map[string]any{},
		}, nil
	}
	snap, err := handle.Snapshot(ctx)
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
