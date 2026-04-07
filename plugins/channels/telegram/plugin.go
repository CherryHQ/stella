package telegram

import (
	"context"
	"fmt"

	internalchannel "github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const (
	PluginID    = internalchannel.TelegramPluginID
	RuntimeName = internalchannel.TelegramRuntimeName
)

var newRuntime = func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error) {
	channelRuntime := host.ChannelRuntime()
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
	notifications := channelRuntime.Notifications()
	var notifier *internalchannel.Dispatcher
	if notifications != nil {
		typed, ok := notifications.(*internalchannel.Dispatcher)
		if !ok {
			return nil, fmt.Errorf("telegram: unsupported notification registry %T", notifications)
		}
		notifier = typed
	}
	_ = host.Logger(PluginID)
	return NewManagedRuntime(RuntimeDeps{
		Parent:   parent,
		Handler:  handler,
		Notifier: notifier,
		NewChannel: func(cfg internalchannel.TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{Token: cfg.Token, ChannelID: cfg.ChannelID, GroupMode: cfg.GroupMode}, handler)
		},
	}), nil
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:                    PluginID,
			Kind:                  config.PluginKindChannel,
			Name:                  internalchannel.PlatformTelegram,
			DisplayName:           "Telegram",
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
		host.Registry().RegisterRuntime(pkgplugins.RuntimeRegistration{PluginID: PluginID, Name: RuntimeName, Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return newRuntime(ctx.Services)
		}})
		host.Registry().RegisterStatus(pkgplugins.StatusRegistration{PluginID: PluginID, Get: func(ctx context.Context) (any, error) {
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
		}})
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
			"enable_notify": map[string]any{
				"type":        "boolean",
				"description": "Whether scheduler and system notifications are delivered to Telegram.",
				"default":     false,
			},
		},
		"required": []any{"token"},
	}
}

// SetRuntimeFactoryForTesting swaps the Telegram managed runtime factory for tests.
func SetRuntimeFactoryForTesting(factory func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error)) func() {
	prev := newRuntime
	newRuntime = func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error) {
		return factory(host)
	}
	return func() { newRuntime = prev }
}
