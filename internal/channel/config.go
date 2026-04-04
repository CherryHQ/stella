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
	switch name {
	case PlatformTelegram:
		cfg := LoadConfig[TelegramConfig](store, name)
		return cfg != nil && cfg.Token != ""
	case PlatformQQ:
		cfg := LoadConfig[QQConfig](store, name)
		return cfg != nil && cfg.AppID != "" && cfg.AppSecret != ""
	case PlatformFeishu:
		cfg := LoadConfig[FeishuConfig](store, name)
		return cfg != nil && cfg.AppID != "" && cfg.AppSecret != ""
	case PlatformWeixin:
		cfg := LoadConfig[WeixinConfig](store, name)
		return cfg != nil && cfg.BotToken != ""
	default:
		return false
	}
}

// IsNotifyEnabled checks whether a channel has notifications enabled.
func IsNotifyEnabled(store config.Store, name string) bool {
	switch name {
	case PlatformTelegram:
		cfg := LoadConfig[TelegramConfig](store, name)
		return cfg != nil && cfg.EnableNotify
	case PlatformQQ:
		cfg := LoadConfig[QQConfig](store, name)
		return cfg != nil && cfg.EnableNotify
	case PlatformFeishu:
		cfg := LoadConfig[FeishuConfig](store, name)
		return cfg != nil && cfg.EnableNotify
	case PlatformWeixin:
		cfg := LoadConfig[WeixinConfig](store, name)
		return cfg != nil && cfg.EnableNotify
	default:
		return false
	}
}
