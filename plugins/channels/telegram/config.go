package telegram

import (
	"encoding/json"
	"fmt"
	"strings"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func DecodeConfig(raw map[string]any) (pkgchannel.TelegramConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return pkgchannel.TelegramConfig{}, fmt.Errorf("encode telegram config: %w", err)
	}
	cfg, err := pkgchannel.DecodeTelegramConfig(data)
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

func validateConfig(cfg pkgchannel.TelegramConfig) string {
	if strings.TrimSpace(cfg.Token) == "" {
		return "telegram: missing token"
	}
	return validateConfigValues(cfg)
}

func validateConfigValues(cfg pkgchannel.TelegramConfig) string {
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
