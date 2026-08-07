package feishu

import (
	"fmt"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	PluginID    = "channel/feishu"
	RuntimeName = "bot"
)

var newRuntime = func(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
	platform := rc.Platform
	channelRuntime := platform.ChannelPlatform()
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
		Log:           platform.Logger(),
		NewChannel: func(cfg pkgchannel.FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID:        cfg.InstanceID,
				AppID:             cfg.AppID,
				AppSecret:         cfg.AppSecret,
				EncryptKey:        cfg.EncryptKey,
				VerificationToken: cfg.VerificationToken,
				Groups:            groupsToPluginConfig(cfg.Groups),
				TenantKey:         cfg.TenantKey,
				AutoProvision:     cfg.AutoProvision,
				AllowedChatIDs:    cfg.AllowedChatIDs,
				AllowDM:           cfg.AllowDM,
				RequireMention:    cfg.RequireMention,
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
				Name:         pkgchannel.PlatformFeishu,
				DisplayName:  "Feishu",
				Description:  "Feishu bot integration.",
				AdminVisible: true,
				Capabilities: []string{
					pkgplugins.CapabilityRuntime,
					pkgplugins.CapabilityConfig,
					pkgplugins.CapabilityStatus,
				},
				RequiredCapabilities: []pkgplugins.Capability{
					pkgplugins.CapabilityChannelPlatform,
					pkgplugins.CapabilityLogger,
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

// SetRuntimeFactoryForTesting swaps the Feishu managed runtime factory for tests.
func SetRuntimeFactoryForTesting(factory func(platform pkgplugins.Platform) (pkgplugins.Runtime, error)) func() {
	prev := newRuntime
	newRuntime = func(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return factory(rc.Platform)
	}
	return func() { newRuntime = prev }
}
