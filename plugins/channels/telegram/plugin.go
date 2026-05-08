package telegram

import (
	"fmt"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	PluginID    = "channel/telegram"
	RuntimeName = "bot"
)

var newRuntime = func(platform pkgplugins.Platform) (pkgplugins.Runtime, error) {
	channelRuntime := platform.ChannelPlatform()
	if channelRuntime == nil {
		return nil, fmt.Errorf("telegram: channel runtime services unavailable")
	}
	parent := channelRuntime.ParentContext()
	if parent == nil {
		return nil, fmt.Errorf("telegram: missing parent context")
	}
	handler := channelRuntime.Handler()
	if handler == nil {
		return nil, fmt.Errorf("telegram: missing channel handler")
	}
	return NewManagedRuntime(RuntimeDeps{
		Parent:        parent,
		Handler:       handler,
		Notifications: channelRuntime.Notifications(),
		NewChannel: func(cfg pkgchannel.TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{InstanceID: cfg.InstanceID, Token: cfg.Token, ChannelID: cfg.ChannelID, GroupMode: cfg.GroupMode}, handler)
		},
	}), nil
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		pkgplugins.RegisterManagedChannelPlugin(host, pkgplugins.ManagedChannelPluginRegistration{
			PluginID:    PluginID,
			RuntimeName: RuntimeName,
			Meta: pkgplugins.PluginInfo{
				ID:           PluginID,
				Kind:         "channel",
				Name:         pkgchannel.PlatformTelegram,
				DisplayName:  "Telegram",
				Description:  "Telegram bot integration.",
				AdminVisible: true,
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
			RuntimeFactory: func(platform pkgplugins.Platform) (pkgplugins.Runtime, error) {
				return newRuntime(platform)
			},
		})
	}))
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"token": map[string]any{
				"type":        "string",
				"description": "Telegram bot token.",
			},
			"channel_id": map[string]any{
				"type":        "string",
				"description": "Optional default channel or chat ID.",
			},
			"group_mode": map[string]any{
				"type":        "string",
				"enum":        []any{"", "mention"},
				"description": "How group chats are handled.",
			},
		},
		"required": []any{"token"},
	}
}

// SetRuntimeFactoryForTesting swaps the Telegram managed runtime factory for tests.
func SetRuntimeFactoryForTesting(factory func(platform pkgplugins.Platform) (pkgplugins.Runtime, error)) func() {
	prev := newRuntime
	newRuntime = func(platform pkgplugins.Platform) (pkgplugins.Runtime, error) {
		return factory(platform)
	}
	return func() { newRuntime = prev }
}
