package telegram

import (
	"encoding/json"
	"fmt"
	"strings"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

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

func DecodeTelegramConfig(data []byte) (TelegramConfig, error) {
	cfg := TelegramConfig{AllowDM: true, GuestMessageLimitPerMinute: pkgchannel.DefaultGuestMessageLimitPerMinute, GuestMaxPerChannel: pkgchannel.DefaultGuestMaxPerChannel, GuestRetentionDays: pkgchannel.DefaultGuestRetentionDays, RequireMention: true}
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

func guestPolicy(raw string) (pkgchannel.GuestConfig, error) {
	cfg, err := DecodeTelegramConfig([]byte(raw))
	policy := pkgchannel.GuestConfig{AllowDM: cfg.AllowDM, AllowUnlinkedDM: cfg.AllowUnlinkedDM, GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute, GuestMaxPerChannel: cfg.GuestMaxPerChannel, GuestRetentionDays: cfg.GuestRetentionDays}
	return policy, err
}

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

func DecodeConfig(raw map[string]any) (TelegramConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return TelegramConfig{}, fmt.Errorf("encode telegram config: %w", err)
	}
	cfg, err := DecodeTelegramConfig(data)
	if err != nil {
		return cfg, fmt.Errorf("decode telegram config: %w", err)
	}
	return cfg, nil
}

func RedactConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return pkgchannel.CloneConfigMap(raw)
	}
	out := pkgchannel.CloneConfigMap(raw)
	if cfg.Token != "" {
		out["token"] = "***"
	}
	return out
}

func validateConfig(cfg TelegramConfig) string {
	if strings.TrimSpace(cfg.Token) == "" {
		return "telegram: missing token"
	}
	return validateConfigValues(cfg)
}

func validateConfigValues(cfg TelegramConfig) string {
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
