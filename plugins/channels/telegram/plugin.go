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

var newRuntime = func(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
	platform := rc.Platform
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
			return New(Config{InstanceID: cfg.InstanceID, Token: cfg.Token, ChannelID: cfg.ChannelID, AllowedChatIDs: cfg.AllowedChatIDs, AllowDM: cfg.AllowDM, RequireMention: cfg.RequireMention}, handler)
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
				RequiredCapabilities: []pkgplugins.Capability{
					pkgplugins.CapabilityChannelPlatform,
					pkgplugins.CapabilityRuntimeLookup,
				},
			},
			DefaultConfig: func() map[string]any {
				return map[string]any{
					"allow_dm": true, "allow_unlinked_dm": false, "require_mention": true,
					"guest_message_limit_per_minute": pkgchannel.DefaultGuestMessageLimitPerMinute,
					"guest_max_per_channel":          pkgchannel.DefaultGuestMaxPerChannel,
					"guest_retention_days":           pkgchannel.DefaultGuestRetentionDays,
				}
			},
			Schema: configSchema(),
			Validate: func(raw map[string]any) error {
				cfg, err := DecodeConfig(raw)
				if err != nil {
					return err
				}
				if msg := validateConfigValues(cfg); msg != "" {
					return fmt.Errorf("%s", msg)
				}
				return nil
			},
			Redact: RedactConfig,
			Configured: func(raw map[string]any) bool {
				cfg, err := DecodeConfig(raw)
				return err == nil && validateConfig(cfg) == ""
			},
			RuntimeFactory: newRuntime,
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
			"allowed_chat_ids":               map[string]any{"type": "string", "description": "Comma-separated Telegram group and supergroup chat IDs allowed to interact with the bot."},
			"allow_dm":                       map[string]any{"type": "boolean", "default": true},
			"allow_unlinked_dm":              map[string]any{"type": "boolean", "default": false},
			"guest_message_limit_per_minute": map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestMessageLimitPerMinute, "default": pkgchannel.DefaultGuestMessageLimitPerMinute},
			"guest_max_per_channel":          map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestMaxPerChannel, "default": pkgchannel.DefaultGuestMaxPerChannel},
			"guest_retention_days":           map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestRetentionDays, "default": pkgchannel.DefaultGuestRetentionDays},
			"require_mention":                map[string]any{"type": "boolean", "default": true},
		},
		"required": []any{"token"},
	}
}

// SetRuntimeFactoryForTesting swaps the Telegram managed runtime factory for tests.
func SetRuntimeFactoryForTesting(factory func(platform pkgplugins.Platform) (pkgplugins.Runtime, error)) func() {
	prev := newRuntime
	newRuntime = func(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return factory(rc.Platform)
	}
	return func() { newRuntime = prev }
}
