package channel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	InstanceID                 string   `json:"-"`
	Token                      string   `json:"token"`
	AllowGroup                 bool     `json:"allow_group"`
	AllowAllGuilds             bool     `json:"allow_all_guilds"`
	AllowedGuildIDs            []string `json:"allowed_guild_ids"`
	AllowedChannelIDs          []string `json:"allowed_channel_ids"`
	AllowedUserIDs             []string `json:"allowed_user_ids"`
	AllowedRoleIDs             []string `json:"allowed_role_ids"`
	AllowDM                    bool     `json:"allow_dm"`
	AllowUnlinkedDM            bool     `json:"allow_unlinked_dm"`
	GuestMessageLimitPerMinute int      `json:"guest_message_limit_per_minute"`
	GuestMaxPerChannel         int      `json:"guest_max_per_channel"`
	GuestRetentionDays         int      `json:"guest_retention_days"`
	RequireMention             bool     `json:"require_mention"`
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
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.AllowedGuildIDs = normalizeIDList(cfg.AllowedGuildIDs)
	cfg.AllowedChannelIDs = normalizeIDList(cfg.AllowedChannelIDs)
	cfg.AllowedUserIDs = normalizeIDList(cfg.AllowedUserIDs)
	cfg.AllowedRoleIDs = normalizeIDList(cfg.AllowedRoleIDs)
	return cfg, nil
}

// normalizeIDList trims whitespace, drops empty entries, and deduplicates
// while preserving first-seen order, so a blank string can never be used to
// bypass an allowlist and equivalent config edits produce identical policy.
func normalizeIDList(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	case PlatformDingTalk:
		cfg, err := DecodeDingTalkConfig([]byte(rawConfig))
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
	InstanceID                 string   `json:"-"`
	Token                      string   `json:"token"`
	ChannelID                  string   `json:"channel_id"`
	AllowGroup                 bool     `json:"allow_group"`
	AllowedChatIDs             []string `json:"allowed_chat_ids"`
	AllowedTopicIDs            []string `json:"allowed_topic_ids"`
	AllowDM                    bool     `json:"allow_dm"`
	AllowUnlinkedDM            bool     `json:"allow_unlinked_dm"`
	GuestMessageLimitPerMinute int      `json:"guest_message_limit_per_minute"`
	GuestMaxPerChannel         int      `json:"guest_max_per_channel"`
	GuestRetentionDays         int      `json:"guest_retention_days"`
	RequireMention             bool     `json:"require_mention"`
	EnableNotify               bool     `json:"enable_notify"`
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
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	var err error
	if cfg.AllowedChatIDs, err = normalizeTelegramAllowlist("allowed_chat_ids", cfg.AllowedChatIDs); err != nil {
		return cfg, err
	}
	if cfg.AllowedTopicIDs, err = normalizeTelegramAllowlist("allowed_topic_ids", cfg.AllowedTopicIDs); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// normalizeTelegramAllowlist keeps an intentionally empty list compatible with
// existing allow-all group behavior, but rejects blank entries. Silently
// turning a configured blank list into an empty allow-all list would widen a
// channel's access boundary.
func normalizeTelegramAllowlist(name string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%s cannot contain blank entries", name)
		}
	}
	return normalizeIDList(ids), nil
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
	AllowGroup                 bool                   `json:"allow_group"`
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

// DingTalkConfig is the persisted DingTalk channel plugin configuration.
type DingTalkConfig struct {
	InstanceID                 string `json:"-"`
	ClientID                   string `json:"client_id"`
	ClientSecret               string `json:"client_secret"`
	AllowGroup                 bool   `json:"allow_group"`
	AllowDM                    bool   `json:"allow_dm"`
	AllowUnlinkedDM            bool   `json:"allow_unlinked_dm"`
	GuestMessageLimitPerMinute int    `json:"guest_message_limit_per_minute"`
	GuestMaxPerChannel         int    `json:"guest_max_per_channel"`
	GuestRetentionDays         int    `json:"guest_retention_days"`
	RequireMention             bool   `json:"require_mention"`
}

// DecodeDingTalkConfig applies stable admission defaults before decoding JSON.
func DecodeDingTalkConfig(data []byte) (DingTalkConfig, error) {
	cfg := DingTalkConfig{
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
