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
	var cfg channel.DiscordConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
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
	return ""
}
