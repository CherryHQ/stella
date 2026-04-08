package feishu

import (
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
		pkgplugins.RegisterManagedChannelPlugin(host, pkgplugins.ManagedChannelPluginRegistration{
			PluginID:    PluginID,
			RuntimeName: RuntimeName,
			Meta: pkgplugins.PluginMeta{
				ID:                    PluginID,
				Kind:                  "channel",
				Name:                  pkgchannel.PlatformFeishu,
				DisplayName:           "Feishu",
				Description:           "Feishu bot integration.",
				AdminVisible:          true,
				SupportsNotifications: true,
				Capabilities: []string{
					pkgplugins.CapabilityRuntime,
					pkgplugins.CapabilityConfig,
					pkgplugins.CapabilityStatus,
				},
			},
			DefaultConfig: func() map[string]any { return map[string]any{} },
			Schema:        configSchema(),
			Validate:      func(raw map[string]any) error { _, err := DecodeConfig(raw); return err },
			Redact:        RedactConfig,
			Configured: func(raw map[string]any) bool {
				cfg, err := DecodeConfig(raw)
				return err == nil && validateConfig(cfg) == ""
			},
			NotificationsEnabled: func(raw map[string]any) bool {
				cfg, err := DecodeConfig(raw)
				return err == nil && cfg.EnableNotify
			},
			RuntimeFactory: newRuntime,
		})
	}))
}
