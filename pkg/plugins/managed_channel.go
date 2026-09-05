package plugins

import (
	"context"

	"github.com/CherryHQ/stella/pkg/channel"
)

type ManagedChannelPluginRegistration struct {
	PluginID          string
	RuntimeName       string
	Meta              PluginInfo
	Info              PluginInfo
	DefaultConfig     func() map[string]any
	Schema            map[string]any
	Validate          func(raw map[string]any) error
	Redact            func(raw map[string]any) map[string]any
	Configured        func(raw map[string]any) bool
	GuestPolicy       channel.GuestPolicyDecoder
	AccountEnrollment bool
	RuntimeFactory    func(rc RuntimeContext) (Runtime, error)
}

func RegisterManagedChannelPlugin(host Host, reg ManagedChannelPluginRegistration) {
	info := reg.Info.Clone()
	if info.ID == "" {
		info = reg.Meta.Clone()
	}
	if info.ID == "" {
		info.ID = reg.PluginID
	}
	info.Managed = true

	host.SetInfo(info)
	host.AddAdmin(AdminSpec{
		PluginID:      reg.PluginID,
		DefaultConfig: reg.DefaultConfig,
		Schema:        reg.Schema,
		Validate:      reg.Validate,
		Redact:        reg.Redact,
		Status: func(ctx context.Context, build AdminContext) (any, error) {
			return managedRuntimeStatus(ctx, build.Platform.RuntimeLookup(), reg.PluginID, reg.RuntimeName)
		},
	})
	host.AddChannel(ChannelSpec{
		PluginID:          reg.PluginID,
		Name:              info.Name,
		Configured:        reg.Configured,
		GuestPolicy:       reg.GuestPolicy,
		AccountEnrollment: reg.AccountEnrollment,
	})
	host.AddRuntime(RuntimeSpec{
		PluginID: reg.PluginID,
		Name:     reg.RuntimeName,
		Build: func(rc RuntimeContext) (Runtime, error) {
			return reg.RuntimeFactory(rc)
		},
	})
}

func managedRuntimeStatus(ctx context.Context, runtime RuntimeLookup, pluginID, runtimeName string) (any, error) {
	handle, ok := runtime.Get(ctx, pluginID, runtimeName)
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
