package dingtalk

import (
	"fmt"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	PluginID    = "channel/dingtalk"
	RuntimeName = "bot"
)

var newRuntime = func(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
	platform := rc.Platform
	channelRuntime := platform.ChannelPlatform()
	if channelRuntime == nil || channelRuntime.ParentContext() == nil || channelRuntime.Handler() == nil {
		return nil, fmt.Errorf("dingtalk: channel runtime services unavailable")
	}
	return NewManagedRuntime(RuntimeDeps{
		Parent:        channelRuntime.ParentContext(),
		Handler:       channelRuntime.Handler(),
		Notifications: channelRuntime.Notifications(),
		WrapHandler:   channelRuntime.WrapHandler(),
		Log:           platform.Logger(),
		NewChannel: func(cfg DingTalkConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID:     cfg.InstanceID,
				ClientID:       cfg.ClientID,
				ClientSecret:   cfg.ClientSecret,
				AllowGroup:     cfg.AllowGroup,
				AllowDM:        cfg.AllowDM,
				RequireMention: cfg.RequireMention,
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
				ID:           PluginID,
				Kind:         "channel",
				Name:         pkgchannel.PlatformDingTalk,
				DisplayName:  "DingTalk",
				Description:  "DingTalk Stream bot integration.",
				AdminVisible: true,
				Capabilities: []string{pkgplugins.CapabilityRuntime, pkgplugins.CapabilityConfig, pkgplugins.CapabilityStatus},
				RequiredCapabilities: []pkgplugins.Capability{
					pkgplugins.CapabilityChannelPlatform,
					pkgplugins.CapabilityLogger,
					pkgplugins.CapabilityRuntimeLookup,
				},
			},
			DefaultConfig: func() map[string]any {
				return map[string]any{
					"allow_group": false, "allow_dm": true, "allow_unlinked_dm": false, "require_mention": true,
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
				if msg := validateConfig(cfg); msg != "" {
					return fmt.Errorf("%s", msg)
				}
				return nil
			},
			Redact:      RedactConfig,
			GuestPolicy: guestPolicy,
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
			"client_id":                      map[string]any{"type": "string", "description": "DingTalk application Client ID."},
			"client_secret":                  map[string]any{"type": "string", "description": "DingTalk application Client Secret."},
			"allow_group":                    map[string]any{"type": "boolean", "description": "Accept messages from DingTalk group conversations the bot is a member of.", "default": false},
			"allow_dm":                       map[string]any{"type": "boolean", "description": "Accept direct messages for linked-user chat and account linking.", "default": true},
			"allow_unlinked_dm":              map[string]any{"type": "boolean", "description": "Allow unlinked DingTalk users to use the bound agent in restricted guest sessions.", "default": false},
			"guest_message_limit_per_minute": map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestMessageLimitPerMinute, "default": pkgchannel.DefaultGuestMessageLimitPerMinute},
			"guest_max_per_channel":          map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestMaxPerChannel, "default": pkgchannel.DefaultGuestMaxPerChannel},
			"guest_retention_days":           map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestRetentionDays, "default": pkgchannel.DefaultGuestRetentionDays},
			"require_mention":                map[string]any{"type": "boolean", "description": "Only process group messages that mention this bot.", "default": true},
		},
		"required": []any{"client_id", "client_secret"},
	}
}
