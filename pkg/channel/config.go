package channel

import (
	"encoding/json"
	"errors"
)

const (
	DefaultGuestMessageLimitPerMinute = 10
	DefaultGuestMaxPerChannel         = 1000
	DefaultGuestRetentionDays         = 30
	MaxGuestMessageLimitPerMinute     = 120
	MaxGuestMaxPerChannel             = 100000
	MaxGuestRetentionDays             = 365
)

// GuestConfig is the platform-independent subset controlling restricted guest
// admission and resource bounds.
type GuestConfig struct {
	AllowDM                    bool
	AllowUnlinkedDM            bool
	GuestMessageLimitPerMinute int
	GuestMaxPerChannel         int
	GuestRetentionDays         int
}

// DiscordConfig is the persisted Discord channel plugin configuration.
type DiscordConfig struct {
	InstanceID                 string `json:"-"`
	Token                      string `json:"token"`
	AllowedGuildIDs            string `json:"allowed_guild_ids"`
	AllowDM                    bool   `json:"allow_dm"`
	AllowUnlinkedDM            bool   `json:"allow_unlinked_dm"`
	GuestMessageLimitPerMinute int    `json:"guest_message_limit_per_minute"`
	GuestMaxPerChannel         int    `json:"guest_max_per_channel"`
	GuestRetentionDays         int    `json:"guest_retention_days"`
	RequireMention             bool   `json:"require_mention"`
}

// DecodeDiscordConfig applies stable defaults before decoding persisted JSON.
func DecodeDiscordConfig(data []byte) (DiscordConfig, error) {
	cfg := DiscordConfig{
		AllowDM:                    true,
		GuestMessageLimitPerMinute: DefaultGuestMessageLimitPerMinute,
		GuestMaxPerChannel:         DefaultGuestMaxPerChannel,
		GuestRetentionDays:         DefaultGuestRetentionDays,
		RequireMention:             true,
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// AllowsUnlinkedGuestDM is the shared fail-closed policy for creating and
// continuing an unlinked guest session through a persisted channel binding.
func AllowsUnlinkedGuestDM(channelType string, enabled bool, rawConfig string) bool {
	if !enabled {
		return false
	}
	cfg, err := DecodeGuestConfig(channelType, rawConfig)
	return err == nil && cfg.AllowDM && cfg.AllowUnlinkedDM
}

// DecodeGuestConfig returns the shared guest policy for a supported channel.
func DecodeGuestConfig(channelType, rawConfig string) (GuestConfig, error) {
	switch channelType {
	case PlatformDiscord:
		cfg, err := DecodeDiscordConfig([]byte(rawConfig))
		return GuestConfig{
			AllowDM:                    cfg.AllowDM,
			AllowUnlinkedDM:            cfg.AllowUnlinkedDM,
			GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute,
			GuestMaxPerChannel:         cfg.GuestMaxPerChannel,
			GuestRetentionDays:         cfg.GuestRetentionDays,
		}, err
	case PlatformTelegram:
		cfg, err := DecodeTelegramConfig([]byte(rawConfig))
		return GuestConfig{
			AllowDM:                    cfg.AllowDM,
			AllowUnlinkedDM:            cfg.AllowUnlinkedDM,
			GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute,
			GuestMaxPerChannel:         cfg.GuestMaxPerChannel,
			GuestRetentionDays:         cfg.GuestRetentionDays,
		}, err
	case PlatformFeishu:
		cfg, err := DecodeFeishuConfig([]byte(rawConfig))
		return GuestConfig{
			AllowDM:                    cfg.AllowDM,
			AllowUnlinkedDM:            cfg.AllowUnlinkedDM,
			GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute,
			GuestMaxPerChannel:         cfg.GuestMaxPerChannel,
			GuestRetentionDays:         cfg.GuestRetentionDays,
		}, err
	default:
		return GuestConfig{}, errors.New("channel does not support guest sessions")
	}
}

// TelegramConfig is the persisted Telegram channel plugin configuration.
type TelegramConfig struct {
	InstanceID                 string `json:"-"`
	Token                      string `json:"token"`
	ChannelID                  string `json:"channel_id"`
	AllowedChatIDs             string `json:"allowed_chat_ids"`
	AllowDM                    bool   `json:"allow_dm"`
	AllowUnlinkedDM            bool   `json:"allow_unlinked_dm"`
	GuestMessageLimitPerMinute int    `json:"guest_message_limit_per_minute"`
	GuestMaxPerChannel         int    `json:"guest_max_per_channel"`
	GuestRetentionDays         int    `json:"guest_retention_days"`
	RequireMention             bool   `json:"require_mention"`
	EnableNotify               bool   `json:"enable_notify"`
}

// DecodeTelegramConfig applies stable admission defaults before decoding JSON.
func DecodeTelegramConfig(data []byte) (TelegramConfig, error) {
	cfg := TelegramConfig{
		AllowDM:                    true,
		GuestMessageLimitPerMinute: DefaultGuestMessageLimitPerMinute,
		GuestMaxPerChannel:         DefaultGuestMaxPerChannel,
		GuestRetentionDays:         DefaultGuestRetentionDays,
		RequireMention:             true,
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// QQConfig is the persisted QQ channel plugin configuration.
type QQConfig struct {
	InstanceID   string `json:"-"`
	AppID        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	EnableNotify bool   `json:"enable_notify"`
}

// FeishuGroup is a per-chat override in the persisted Feishu channel config.
type FeishuGroup struct {
	SystemPrompt string   `json:"system_prompt"`
	ToolAllow    []string `json:"tool_allow"`
	ToolDeny     []string `json:"tool_deny"`
}

// FeishuConfig is the persisted Feishu channel plugin configuration.
type FeishuConfig struct {
	InstanceID                 string                 `json:"-"`
	AppID                      string                 `json:"app_id"`
	AppSecret                  string                 `json:"app_secret"`
	EncryptKey                 string                 `json:"encrypt_key"`
	VerificationToken          string                 `json:"verification_token"`
	Groups                     map[string]FeishuGroup `json:"groups"`
	AllowedChatIDs             string                 `json:"allowed_chat_ids"`
	AllowDM                    bool                   `json:"allow_dm"`
	AllowUnlinkedDM            bool                   `json:"allow_unlinked_dm"`
	GuestMessageLimitPerMinute int                    `json:"guest_message_limit_per_minute"`
	GuestMaxPerChannel         int                    `json:"guest_max_per_channel"`
	GuestRetentionDays         int                    `json:"guest_retention_days"`
	RequireMention             bool                   `json:"require_mention"`
	EnableNotify               bool                   `json:"enable_notify"`
	TenantKey                  string                 `json:"tenant_key"`
	AutoProvision              bool                   `json:"auto_provision"`
}

// DecodeFeishuConfig applies stable admission defaults before decoding JSON.
func DecodeFeishuConfig(data []byte) (FeishuConfig, error) {
	cfg := FeishuConfig{
		AllowDM:                    true,
		GuestMessageLimitPerMinute: DefaultGuestMessageLimitPerMinute,
		GuestMaxPerChannel:         DefaultGuestMaxPerChannel,
		GuestRetentionDays:         DefaultGuestRetentionDays,
		RequireMention:             true,
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// WeixinConfig is the persisted Weixin channel plugin configuration.
type WeixinConfig struct {
	InstanceID   string `json:"-"`
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"base_url"`
	BotID        string `json:"bot_id"`
	UserID       string `json:"user_id"`
	SKRouteTag   string `json:"sk_route_tag"`
	EnableNotify bool   `json:"enable_notify"`
}
