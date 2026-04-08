package reflect

import (
	"context"
	"fmt"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

var newRuntime = func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error) {
	deps, err := runtimeDeps(host)
	if err != nil {
		return nil, err
	}
	return NewManagedRuntime(deps), nil
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:           PluginID,
			Kind:         "reflect",
			Name:         "reflect",
			DisplayName:  "Reflect",
			Description:  "Background conversation review and profile extraction.",
			Managed:      true,
			AdminVisible: true,
			HasConfig:    true,
			HasStatus:    true,
			Capabilities: []string{
				pkgplugins.CapabilityRuntime,
				pkgplugins.CapabilityConfig,
				pkgplugins.CapabilityStatus,
			},
		})
		host.Registry().RegisterConfig(pkgplugins.ConfigRegistration{
			PluginID:      PluginID,
			DefaultConfig: DefaultPluginConfig,
			Schema:        PluginConfigSchema(),
			Validate:      func(raw map[string]any) error { _, err := DecodePluginConfig(raw); return err },
			Redact:        RedactPluginConfig,
		})
		host.Registry().RegisterRuntime(pkgplugins.RuntimeRegistration{
			PluginID: PluginID,
			Name:     RuntimeName,
			Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
				return newRuntime(ctx.Services)
			},
		})
		host.Registry().RegisterStatus(pkgplugins.StatusRegistration{
			PluginID: PluginID,
			Get: func(ctx context.Context) (any, error) {
				handle, ok := host.Services().Runtime().Get(PluginID, RuntimeName)
				if !ok {
					return stoppedStatus(), nil
				}
				snap, err := handle.Snapshot(ctx)
				if err != nil {
					return nil, err
				}
				return runtimeStatus(snap), nil
			},
		})
	}))
}

func SetRuntimeFactoryForTesting(factory func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error)) func() {
	prev := newRuntime
	newRuntime = factory
	return func() { newRuntime = prev }
}

func runtimeDeps(host pkgplugins.ServiceHost) (RuntimeDeps, error) {
	services := host.ReflectRuntime()
	if services == nil {
		return RuntimeDeps{}, fmt.Errorf("reflect: runtime services unavailable")
	}
	if services.Memory() == nil {
		return RuntimeDeps{}, fmt.Errorf("reflect: missing memory provider")
	}
	if services.Store() == nil {
		return RuntimeDeps{}, fmt.Errorf("reflect: missing config store")
	}
	if services.ParentContext() == nil {
		return RuntimeDeps{}, fmt.Errorf("reflect: missing parent context")
	}
	if host.StateStore() == nil {
		return RuntimeDeps{}, fmt.Errorf("reflect: missing plugin state store")
	}
	return RuntimeDeps{
		Services:      services,
		Notifications: host.Notifications(),
		StateStore:    host.StateStore(),
		Log:           host.Logger(PluginID),
	}, nil
}

func stoppedStatus() map[string]any {
	return map[string]any{
		"state":      pkgplugins.RuntimeStateStopped,
		"updated_at": nil,
		"metadata":   map[string]any{},
	}
}

func runtimeStatus(snap pkgplugins.RuntimeSnapshot) map[string]any {
	return map[string]any{
		"state":      snap.State,
		"message":    snap.Message,
		"updated_at": snap.UpdatedAt,
		"metadata":   snap.Metadata,
	}
}
