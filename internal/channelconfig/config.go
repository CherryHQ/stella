package channelconfig

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

type channelAccess struct {
	hasValid      func(config.Store, string) bool
	notifyEnabled func(config.Store, string) bool
}

var accessors = map[string]channelAccess{
	pkgchannel.PlatformTelegram: {
		hasValid: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.TelegramConfig](store, name)
			return cfg != nil && cfg.Token != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.TelegramConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
	pkgchannel.PlatformQQ: {
		hasValid: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.QQConfig](store, name)
			return cfg != nil && cfg.AppID != "" && cfg.AppSecret != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.QQConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
	pkgchannel.PlatformFeishu: {
		hasValid: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.FeishuConfig](store, name)
			return cfg != nil && cfg.AppID != "" && cfg.AppSecret != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.FeishuConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
	pkgchannel.PlatformWeixin: {
		hasValid: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.WeixinConfig](store, name)
			return cfg != nil && cfg.BotToken != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := Load[pkgchannel.WeixinConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
}

// Load loads a channel plugin config from the settings_plugins table and
// deserializes it into the given type. Returns nil if the plugin is missing,
// disabled, or the payload cannot be decoded.
func Load[T any](store config.Store, channelID string) *T {
	pluginID := config.PluginID(config.PluginKindChannel, channelID)
	p, err := store.GetPlugin(context.Background(), pluginID)
	if err != nil || !p.Enabled {
		return nil
	}

	data, err := json.Marshal(p.Config)
	if err != nil {
		slog.Warn("failed to marshal plugin config", "plugin", pluginID, "error", err)
		return nil
	}
	var cfg T
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse plugin config", "plugin", pluginID, "error", err)
		return nil
	}
	return &cfg
}

// HasValidConfig checks whether a channel has required credentials configured.
func HasValidConfig(store config.Store, name string) bool {
	access, ok := accessors[name]
	if !ok || access.hasValid == nil {
		return false
	}
	return access.hasValid(store, name)
}

// IsNotifyEnabled checks whether a channel has notifications enabled.
func IsNotifyEnabled(store config.Store, name string) bool {
	access, ok := accessors[name]
	if !ok || access.notifyEnabled == nil {
		return false
	}
	return access.notifyEnabled(store, name)
}
