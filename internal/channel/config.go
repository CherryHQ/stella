package channel

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/vaayne/anna/internal/config"
)

type TelegramConfig struct {
	Token        string `json:"token"`
	ChannelID    string `json:"channel_id"`
	GroupMode    string `json:"group_mode"`
	EnableNotify bool   `json:"enable_notify"`
}

type QQConfig struct {
	AppID        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	GroupMode    string `json:"group_mode"`
	EnableNotify bool   `json:"enable_notify"`
}

type FeishuConfig struct {
	AppID             string                 `json:"app_id"`
	AppSecret         string                 `json:"app_secret"`
	EncryptKey        string                 `json:"encrypt_key"`
	VerificationToken string                 `json:"verification_token"`
	GroupMode         string                 `json:"group_mode"`
	Groups            map[string]FeishuGroup `json:"groups"`
	EnableNotify      bool                   `json:"enable_notify"`
}

type FeishuGroup struct {
	GroupMode    string   `json:"group_mode"`
	SystemPrompt string   `json:"system_prompt"`
	ToolAllow    []string `json:"tool_allow"`
	ToolDeny     []string `json:"tool_deny"`
}

type WeixinConfig struct {
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"base_url"`
	BotID        string `json:"bot_id"`
	UserID       string `json:"user_id"`
	EnableNotify bool   `json:"enable_notify"`
}

type channelConfigAccess struct {
	hasValid      func(config.Store, string) bool
	notifyEnabled func(config.Store, string) bool
}

var channelConfigAccessors = map[string]channelConfigAccess{
	PlatformTelegram: {
		hasValid: func(store config.Store, name string) bool {
			cfg := LoadConfig[TelegramConfig](store, name)
			return cfg != nil && cfg.Token != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := LoadConfig[TelegramConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
	PlatformQQ: {
		hasValid: func(store config.Store, name string) bool {
			cfg := LoadConfig[QQConfig](store, name)
			return cfg != nil && cfg.AppID != "" && cfg.AppSecret != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := LoadConfig[QQConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
	PlatformFeishu: {
		hasValid: func(store config.Store, name string) bool {
			cfg := LoadConfig[FeishuConfig](store, name)
			return cfg != nil && cfg.AppID != "" && cfg.AppSecret != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := LoadConfig[FeishuConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
	PlatformWeixin: {
		hasValid: func(store config.Store, name string) bool {
			cfg := LoadConfig[WeixinConfig](store, name)
			return cfg != nil && cfg.BotToken != ""
		},
		notifyEnabled: func(store config.Store, name string) bool {
			cfg := LoadConfig[WeixinConfig](store, name)
			return cfg != nil && cfg.EnableNotify
		},
	},
}

func DecodePluginConfig[T any](raw map[string]any, plugin string) (T, error) {
	return decodePluginConfig[T](raw, plugin)
}

func CloneConfigMap(raw map[string]any) map[string]any {
	return cloneConfigMap(raw)
}

// LoadConfig loads a channel plugin's config from the settings_plugins table
// and deserializes it into the given type. Returns nil if the plugin is missing,
// disabled, or the payload cannot be decoded.
func LoadConfig[T any](store config.Store, channelID string) *T {
	pluginID := config.PluginID(config.PluginKindChannel, channelID)
	p, err := store.GetPlugin(context.Background(), pluginID)
	if err != nil || !p.Enabled {
		return nil
	}
	// Convert map[string]any -> JSON -> typed config.
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
	access, ok := channelConfigAccessors[name]
	if !ok || access.hasValid == nil {
		return false
	}
	return access.hasValid(store, name)
}

// IsNotifyEnabled checks whether a channel has notifications enabled.
func IsNotifyEnabled(store config.Store, name string) bool {
	access, ok := channelConfigAccessors[name]
	if !ok || access.notifyEnabled == nil {
		return false
	}
	return access.notifyEnabled(store, name)
}
