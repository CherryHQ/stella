package feishu

import (
	"context"
	"fmt"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const (
	PluginID    = "channel/feishu"
	RuntimeName = "bot"
)

var newRuntime = func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error) {
	channelRuntime := host.ChannelRuntime()
	if channelRuntime == nil {
		return nil, fmt.Errorf("feishu: channel runtime services unavailable")
	}
	parent := channelRuntime.ParentContext()
	if parent == nil {
		return nil, fmt.Errorf("feishu: missing parent context")
	}
	handler := channelRuntime.Handler()
	if handler == nil {
		return nil, fmt.Errorf("feishu: missing channel handler")
	}
	return NewFeishuManagedRuntime(FeishuRuntimeDeps{
		Parent:        parent,
		Handler:       handler,
		Notifications: channelRuntime.Notifications(),
		Log:           host.Logger(PluginID),
		NewChannel: func(cfg pkgchannel.FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				AppID:             cfg.AppID,
				AppSecret:         cfg.AppSecret,
				EncryptKey:        cfg.EncryptKey,
				VerificationToken: cfg.VerificationToken,
				GroupMode:         cfg.GroupMode,
				Groups:            groupsToPluginConfig(cfg.Groups),
			}, handler)
		},
	}), nil
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:                    PluginID,
			Kind:                  "channel",
			Name:                  pkgchannel.PlatformFeishu,
			DisplayName:           "Feishu",
			Description:           "Feishu bot integration.",
			Managed:               true,
			AdminVisible:          true,
			HasConfig:             true,
			HasStatus:             true,
			SupportsNotifications: true,
			Capabilities: []string{
				pkgplugins.CapabilityRuntime,
				pkgplugins.CapabilityConfig,
				pkgplugins.CapabilityStatus,
			},
		})
		host.Registry().RegisterConfig(pkgplugins.ConfigRegistration{
			PluginID:      PluginID,
			DefaultConfig: func() map[string]any { return map[string]any{} },
			Schema:        configSchema(),
			Validate:      func(raw map[string]any) error { _, err := DecodeConfig(raw); return err },
			Redact:        RedactConfig,
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
					return map[string]any{
						"state":      pkgplugins.RuntimeStateStopped,
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
			},
		})
	}))
}
