package dingtalk

import (
	"encoding/json"
	"fmt"
	"strings"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

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

func DecodeDingTalkConfig(data []byte) (DingTalkConfig, error) {
	cfg := DingTalkConfig{AllowDM: true, GuestMessageLimitPerMinute: pkgchannel.DefaultGuestMessageLimitPerMinute, GuestMaxPerChannel: pkgchannel.DefaultGuestMaxPerChannel, GuestRetentionDays: pkgchannel.DefaultGuestRetentionDays, RequireMention: true}
	return cfg, json.Unmarshal(data, &cfg)
}

func guestPolicy(raw string) (pkgchannel.GuestConfig, error) {
	cfg, err := DecodeDingTalkConfig([]byte(raw))
	policy := pkgchannel.GuestConfig{AllowDM: cfg.AllowDM, AllowUnlinkedDM: cfg.AllowUnlinkedDM, GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute, GuestMaxPerChannel: cfg.GuestMaxPerChannel, GuestRetentionDays: cfg.GuestRetentionDays}
	return policy, err
}

func DecodeConfig(raw map[string]any) (DingTalkConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return DingTalkConfig{}, fmt.Errorf("encode dingtalk config: %w", err)
	}
	cfg, err := DecodeDingTalkConfig(data)
	if err != nil {
		return cfg, fmt.Errorf("decode dingtalk config: %w", err)
	}
	return cfg, nil
}

func validateConfig(cfg DingTalkConfig) string {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return "dingtalk: missing client_id or client_secret"
	}
	if cfg.GuestMessageLimitPerMinute < 1 || cfg.GuestMessageLimitPerMinute > pkgchannel.MaxGuestMessageLimitPerMinute {
		return fmt.Sprintf("guest message limit per minute must be between 1 and %d", pkgchannel.MaxGuestMessageLimitPerMinute)
	}
	if cfg.GuestMaxPerChannel < 1 || cfg.GuestMaxPerChannel > pkgchannel.MaxGuestMaxPerChannel {
		return fmt.Sprintf("guest maximum per channel must be between 1 and %d", pkgchannel.MaxGuestMaxPerChannel)
	}
	if cfg.GuestRetentionDays < 1 || cfg.GuestRetentionDays > pkgchannel.MaxGuestRetentionDays {
		return fmt.Sprintf("guest retention days must be between 1 and %d", pkgchannel.MaxGuestRetentionDays)
	}
	return ""
}
