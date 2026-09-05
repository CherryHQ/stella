package feishu

import (
	"encoding/json"
	"fmt"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type FeishuGroup struct {
	SystemPrompt    string   `json:"system_prompt"`
	ToolAllow       []string `json:"tool_allow"`
	ToolDeny        []string `json:"tool_deny"`
	Enabled         *bool    `json:"enabled,omitempty"`
	RequireMention  *bool    `json:"require_mention,omitempty"`
	AllowedUsers    []string `json:"allowed_users,omitempty"`
	DisallowedUsers []string `json:"disallowed_users,omitempty"`
}

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

func DecodeFeishuConfig(data []byte) (FeishuConfig, error) {
	cfg := FeishuConfig{AllowDM: true, GuestMessageLimitPerMinute: pkgchannel.DefaultGuestMessageLimitPerMinute, GuestMaxPerChannel: pkgchannel.DefaultGuestMaxPerChannel, GuestRetentionDays: pkgchannel.DefaultGuestRetentionDays, RequireMention: true}
	return cfg, json.Unmarshal(data, &cfg)
}

func guestPolicy(raw string) (pkgchannel.GuestConfig, error) {
	cfg, err := DecodeFeishuConfig([]byte(raw))
	policy := pkgchannel.GuestConfig{AllowDM: cfg.AllowDM, AllowUnlinkedDM: cfg.AllowUnlinkedDM, GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute, GuestMaxPerChannel: cfg.GuestMaxPerChannel, GuestRetentionDays: cfg.GuestRetentionDays}
	return policy, err
}

func DecodeConfig(raw map[string]any) (FeishuConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return FeishuConfig{}, fmt.Errorf("encode feishu config: %w", err)
	}
	cfg, err := DecodeFeishuConfig(data)
	if err != nil {
		return cfg, fmt.Errorf("decode feishu config: %w", err)
	}
	return cfg, nil
}
