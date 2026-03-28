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

// LoadConfig loads a channel's JSON config from the store and deserializes it
// into the given type. Returns nil if the channel is missing, disabled, or the
// payload cannot be decoded.
func LoadConfig[T any](store config.Store, channelID string) *T {
	ch, err := store.GetChannel(context.Background(), channelID)
	if err != nil || !ch.Enabled {
		return nil
	}
	var cfg T
	if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
		slog.Warn("failed to parse channel config", "channel", channelID, "error", err)
		return nil
	}
	return &cfg
}
