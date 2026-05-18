package reflect

import (
	"context"
	"fmt"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

var newRuntime = func(platform pkgplugins.Platform) (pkgplugins.Runtime, error) {
	deps, err := runtimeDeps(platform)
	if err != nil {
		return nil, err
	}
	return NewManagedRuntime(deps), nil
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
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
		host.AddAdmin(pkgplugins.AdminSpec{
			PluginID:      PluginID,
			DefaultConfig: DefaultPluginConfig,
			Schema:        PluginConfigSchema(),
			Validate:      func(raw map[string]any) error { _, err := DecodePluginConfig(raw); return err },
			Redact:        RedactPluginConfig,
		})
		host.AddRuntime(pkgplugins.RuntimeSpec{
			PluginID: PluginID,
			Name:     RuntimeName,
			Build: func(ctx pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
				return newRuntime(ctx.Platform)
			},
		})
		host.AddAdmin(pkgplugins.AdminSpec{
			PluginID: PluginID,
			Status: func(ctx context.Context, build pkgplugins.AdminContext) (any, error) {
				return runtimeStatusFromLookup(ctx, build.Platform.RuntimeLookup())
			},
		})
	}))
}

func SetRuntimeFactoryForTesting(factory func(platform pkgplugins.Platform) (pkgplugins.Runtime, error)) func() {
	prev := newRuntime
	newRuntime = factory
	return func() { newRuntime = prev }
}

func runtimeDeps(platform pkgplugins.Platform) (RuntimeDeps, error) {
	services := platform.ReflectPlatform()
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
	if platform.StateStore() == nil {
		return RuntimeDeps{}, fmt.Errorf("reflect: missing plugin state store")
	}
	return RuntimeDeps{
		Services:      services,
		Notifications: platform.Notifier(),
		StateStore:    platform.StateStore(),
		SkillStore:    platform.SkillStore(),
		Scheduler:     platform.Scheduler(),
		Log:           platform.Logger(),
	}, nil
}

func runtimeStatusFromLookup(ctx context.Context, lookup pkgplugins.RuntimeLookup) (any, error) {
	if lookup == nil {
		return stoppedStatus(), nil
	}
	handle, ok := lookup.Lookup(PluginID, RuntimeName)
	if !ok {
		return stoppedStatus(), nil
	}
	snap, err := handle.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return runtimeStatus(snap), nil
}

func stoppedStatus() map[string]any {
	return map[string]any{
		"state":      pkgplugins.RuntimeStateStopped,
		"updated_at": nil,
		"metadata":   map[string]any{},
	}
}

func runtimeStatus(snap pkgplugins.RuntimeStatus) map[string]any {
	return map[string]any{
		"state":      snap.State,
		"message":    snap.Message,
		"updated_at": snap.UpdatedAt,
		"metadata":   snap.Metadata,
	}
}
