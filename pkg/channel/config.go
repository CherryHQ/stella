package channel

import "encoding/json"

const (
	DefaultDiscordGuestMessageLimitPerMinute = 10
	DefaultDiscordGuestMaxPerChannel         = 1000
	DefaultDiscordGuestRetentionDays         = 30
	MaxDiscordGuestMessageLimitPerMinute     = 120
	MaxDiscordGuestMaxPerChannel             = 100000
	MaxDiscordGuestRetentionDays             = 365
)

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
		GuestMessageLimitPerMinute: DefaultDiscordGuestMessageLimitPerMinute,
		GuestMaxPerChannel:         DefaultDiscordGuestMaxPerChannel,
		GuestRetentionDays:         DefaultDiscordGuestRetentionDays,
		RequireMention:             true,
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// AllowsUnlinkedGuestDM is the shared fail-closed policy for creating and
// continuing an unlinked guest session through a persisted channel binding.
func AllowsUnlinkedGuestDM(channelType string, enabled bool, rawConfig string) bool {
	if !enabled || channelType != PlatformDiscord {
		return false
	}
	cfg, err := DecodeDiscordConfig([]byte(rawConfig))
	return err == nil && cfg.AllowDM && cfg.AllowUnlinkedDM
}

// TelegramConfig is the persisted Telegram channel plugin configuration.
type TelegramConfig struct {
	InstanceID   string `json:"-"`
	Token        string `json:"token"`
	ChannelID    string `json:"channel_id"`
	EnableNotify bool   `json:"enable_notify"`
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
	InstanceID        string                 `json:"-"`
	AppID             string                 `json:"app_id"`
	AppSecret         string                 `json:"app_secret"`
	EncryptKey        string                 `json:"encrypt_key"`
	VerificationToken string                 `json:"verification_token"`
	Groups            map[string]FeishuGroup `json:"groups"`
	EnableNotify      bool                   `json:"enable_notify"`
	TenantKey         string                 `json:"tenant_key"`
	AutoProvision     bool                   `json:"auto_provision"`
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
