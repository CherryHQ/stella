package discord

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/CherryHQ/stella/pkg/channel"
)

func DecodeConfig(raw map[string]any) (channel.DiscordConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return channel.DiscordConfig{}, fmt.Errorf("encode discord config: %w", err)
	}
	cfg, err := channel.DecodeDiscordConfig(data)
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

func validateConfig(cfg channel.DiscordConfig) string {
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
