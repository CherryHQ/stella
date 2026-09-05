package discord

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/CherryHQ/stella/pkg/channel"
)

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

func DecodeDiscordConfig(data []byte) (DiscordConfig, error) {
	cfg := DiscordConfig{AllowDM: true, GuestMessageLimitPerMinute: channel.DefaultGuestMessageLimitPerMinute, GuestMaxPerChannel: channel.DefaultGuestMaxPerChannel, GuestRetentionDays: channel.DefaultGuestRetentionDays, RequireMention: true}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.AllowedGuildIDs = normalizeIDList(cfg.AllowedGuildIDs)
	cfg.AllowedChannelIDs = normalizeIDList(cfg.AllowedChannelIDs)
	cfg.AllowedUserIDs = normalizeIDList(cfg.AllowedUserIDs)
	cfg.AllowedRoleIDs = normalizeIDList(cfg.AllowedRoleIDs)
	return cfg, nil
}

func guestPolicy(raw string) (channel.GuestConfig, error) {
	cfg, err := DecodeDiscordConfig([]byte(raw))
	policy := channel.GuestConfig{AllowDM: cfg.AllowDM, AllowUnlinkedDM: cfg.AllowUnlinkedDM, GuestMessageLimitPerMinute: cfg.GuestMessageLimitPerMinute, GuestMaxPerChannel: cfg.GuestMaxPerChannel, GuestRetentionDays: cfg.GuestRetentionDays}
	return policy, err
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

func DecodeConfig(raw map[string]any) (DiscordConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return DiscordConfig{}, fmt.Errorf("encode discord config: %w", err)
	}
	cfg, err := DecodeDiscordConfig(data)
	if err != nil {
		return cfg, fmt.Errorf("decode discord config: %w", err)
	}
	return cfg, nil
}

func RedactConfig(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	maps.Copy(out, raw)
	if _, ok := out["token"]; ok {
		out["token"] = "***"
	}
	return out
}

func validateConfig(cfg DiscordConfig) string {
	if strings.TrimSpace(cfg.Token) == "" {
		return "discord bot token is required"
	}
	if cfg.GuestMessageLimitPerMinute < 1 || cfg.GuestMessageLimitPerMinute > channel.MaxGuestMessageLimitPerMinute {
		return fmt.Sprintf("guest message limit per minute must be between 1 and %d", channel.MaxGuestMessageLimitPerMinute)
	}
	if cfg.GuestMaxPerChannel < 1 || cfg.GuestMaxPerChannel > channel.MaxGuestMaxPerChannel {
		return fmt.Sprintf("guest maximum per channel must be between 1 and %d", channel.MaxGuestMaxPerChannel)
	}
	if cfg.GuestRetentionDays < 1 || cfg.GuestRetentionDays > channel.MaxGuestRetentionDays {
		return fmt.Sprintf("guest retention days must be between 1 and %d", channel.MaxGuestRetentionDays)
	}
	return ""
}
