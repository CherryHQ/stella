package discord

import (
	"fmt"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/plugins"
)

const (
	PluginID    = "channel/discord"
	RuntimeName = "bot"
)

var newRuntime = func(rc plugins.RuntimeContext) (plugins.Runtime, error) {
	platform := rc.Platform
	r := platform.ChannelPlatform()
	if r == nil || r.ParentContext() == nil || r.Handler() == nil {
		return nil, fmt.Errorf("discord: channel runtime services unavailable")
	}
	return NewManagedRuntime(RuntimeDeps{Parent: r.ParentContext(), Handler: r.Handler(), Notifications: r.Notifications(), Log: platform.Logger(), NewChannel: func(cfg channel.DiscordConfig, h channel.Handler) (channel.Channel, error) {
		return New(Config{InstanceID: cfg.InstanceID, Token: cfg.Token, AllowGroup: cfg.AllowGroup, AllowDM: cfg.AllowDM, RequireMention: cfg.RequireMention}, h)
	}}), nil
}

func init() {
	plugins.Register(PluginID, plugins.PluginFunc(func(host plugins.Host) {
		plugins.RegisterManagedChannelPlugin(host, plugins.ManagedChannelPluginRegistration{
			PluginID: PluginID, RuntimeName: RuntimeName,
			Meta: plugins.PluginInfo{
				ID: PluginID, Kind: "channel", Name: channel.PlatformDiscord, DisplayName: "Discord", Description: "Discord bot integration.", AdminVisible: true,
				Capabilities: []string{plugins.CapabilityRuntime, plugins.CapabilityConfig, plugins.CapabilityStatus}, RequiredCapabilities: []plugins.Capability{plugins.CapabilityChannelPlatform, plugins.CapabilityLogger, plugins.CapabilityRuntimeLookup},
			},
			DefaultConfig: func() map[string]any {
				return map[string]any{
					"allow_group": false, "allow_dm": true, "allow_unlinked_dm": false, "require_mention": true,
					"guest_message_limit_per_minute": channel.DefaultGuestMessageLimitPerMinute,
					"guest_max_per_channel":          channel.DefaultGuestMaxPerChannel,
					"guest_retention_days":           channel.DefaultGuestRetentionDays,
				}
			}, Schema: configSchema(), Validate: func(raw map[string]any) error {
				cfg, err := DecodeConfig(raw)
				if err != nil {
					return err
				}
				if msg := validateConfig(cfg); msg != "" {
					return fmt.Errorf("%s", msg)
				}
				return nil
			},
			Redact: RedactConfig, Configured: func(raw map[string]any) bool {
				cfg, err := DecodeConfig(raw)
				return err == nil && validateConfig(cfg) == ""
			}, RuntimeFactory: newRuntime,
		})
	}))
}

func configSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"token":                          map[string]any{"type": "string", "description": "Discord bot token."},
		"allow_group":                    map[string]any{"type": "boolean", "description": "Accept messages from server channels in the servers this bot joined.", "default": false},
		"allow_dm":                       map[string]any{"type": "boolean", "description": "Accept direct messages for linked-user chat and account linking.", "default": true},
		"allow_unlinked_dm":              map[string]any{"type": "boolean", "description": "Allow unlinked Discord users to use the bound agent in restricted guest sessions.", "default": false},
		"guest_message_limit_per_minute": map[string]any{"type": "integer", "description": "Maximum accepted guest messages per minute and guest.", "minimum": 1, "maximum": channel.MaxGuestMessageLimitPerMinute, "default": channel.DefaultGuestMessageLimitPerMinute},
		"guest_max_per_channel":          map[string]any{"type": "integer", "description": "Maximum durable guest identities for this channel.", "minimum": 1, "maximum": channel.MaxGuestMaxPerChannel, "default": channel.DefaultGuestMaxPerChannel},
		"guest_retention_days":           map[string]any{"type": "integer", "description": "Delete guest identities and sessions after this many inactive days.", "minimum": 1, "maximum": channel.MaxGuestRetentionDays, "default": channel.DefaultGuestRetentionDays},
		"require_mention":                map[string]any{"type": "boolean", "description": "Only process guild messages that mention this bot.", "default": true},
	}, "required": []any{"token"}}
}
