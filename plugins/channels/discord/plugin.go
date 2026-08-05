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
		return New(Config{InstanceID: cfg.InstanceID, Token: cfg.Token}, h)
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
			DefaultConfig: func() map[string]any { return map[string]any{} }, Schema: configSchema(), Validate: func(raw map[string]any) error {
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
	return map[string]any{"type": "object", "properties": map[string]any{"token": map[string]any{"type": "string", "description": "Discord bot token."}}, "required": []any{"token"}}
}
