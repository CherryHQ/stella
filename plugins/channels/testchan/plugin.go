package testchan

import (
	"fmt"
	"os"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func newRuntime(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
	platform := rc.Platform
	channelRuntime := platform.ChannelPlatform()
	if channelRuntime == nil {
		return nil, fmt.Errorf("testchan: channel runtime services unavailable")
	}
	parent := channelRuntime.ParentContext()
	if parent == nil {
		return nil, fmt.Errorf("testchan: missing parent context")
	}
	handler := channelRuntime.Handler()
	if handler == nil {
		return nil, fmt.Errorf("testchan: missing channel handler")
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[Config]{
		Parent:          parent,
		Handler:         handler,
		Notifier:        channelRuntime.Notifications(),
		Platform:        Platform,
		DecodeConfig:    DecodeConfig,
		ConfigureConfig: configureConfig,
		ValidateConfig:  validateConfig,
		NewChannel:      newChannel,
		Snapshot:        runtimeSnapshot,
		WrapHandler:     channelRuntime.WrapHandler(),
	}), nil
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg Config) pkgplugins.RuntimeStatus {
	return pkgplugins.RuntimeStatus{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata:  map[string]any{"instance_id": cfg.InstanceID},
	}
}

func init() {
	// Test-only adapter: never visible in production builds unless the
	// environment opts in explicitly.
	if os.Getenv("STELLA_TEST_CHANNELS") == "" {
		return
	}
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		pkgplugins.RegisterManagedChannelPlugin(host, pkgplugins.ManagedChannelPluginRegistration{
			PluginID:    PluginID,
			RuntimeName: RuntimeName,
			Info: pkgplugins.PluginInfo{
				ID:          PluginID,
				Kind:        "channel",
				Name:        Platform,
				DisplayName: "Test Channel",
				Description: "Test-only HTTP channel adapter for multi-replica verification.",
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
				return map[string]any{"allow_group": false, "allow_dm": true, "allow_unlinked_dm": true}
			},
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"endpoint":          map[string]any{"type": "string", "description": "Fake platform base URL."},
					"bot_name":          map[string]any{"type": "string", "description": "Bot account name."},
					"allow_dm":          map[string]any{"type": "boolean", "default": true},
					"allow_unlinked_dm": map[string]any{"type": "boolean", "default": true},
				},
				"required": []any{"endpoint"},
			},
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
			Configured: func(raw map[string]any) bool {
				cfg, err := DecodeConfig(raw)
				return err == nil && validateConfig(cfg) == ""
			},
			RuntimeFactory: newRuntime,
		})
	}))
}
