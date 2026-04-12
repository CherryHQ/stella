package qq

import (
	"fmt"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const (
	PluginID    = "channel/qq"
	RuntimeName = "bot"
)

var newRuntime = func(platform pkgplugins.Platform) (pkgplugins.Runtime, error) {
	channelRuntime := platform.ChannelPlatform()
	if channelRuntime == nil {
		return nil, fmt.Errorf("qq: channel runtime services unavailable")
	}
	parent := channelRuntime.ParentContext()
	if parent == nil {
		return nil, fmt.Errorf("qq: missing parent context")
	}
	handler := channelRuntime.Handler()
	if handler == nil {
		return nil, fmt.Errorf("qq: missing channel handler")
	}
	return NewQQManagedRuntime(QQRuntimeDeps{
		Parent:        parent,
		Handler:       handler,
		Notifications: channelRuntime.Notifications(),
		Log:           platform.Logger(),
		NewChannel: func(cfg pkgchannel.QQConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID: cfg.InstanceID,
				AppID:      cfg.AppID,
				AppSecret:  cfg.AppSecret,
				GroupMode:  cfg.GroupMode,
			}, handler)
		},
	}), nil
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		pkgplugins.RegisterManagedChannelPlugin(host, pkgplugins.ManagedChannelPluginRegistration{
			PluginID:    PluginID,
			RuntimeName: RuntimeName,
			Meta: pkgplugins.PluginInfo{
				ID:                    PluginID,
				Kind:                  "channel",
				Name:                  pkgchannel.PlatformQQ,
				DisplayName:           "QQ",
				Description:           "QQ bot integration.",
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
