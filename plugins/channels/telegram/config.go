package telegram

import (
	internalchannel "github.com/vaayne/anna/internal/channel"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

func DecodeConfig(raw map[string]any) (pkgchannel.TelegramConfig, error) {
	return internalchannel.DecodePluginConfig[pkgchannel.TelegramConfig](raw, pkgchannel.PlatformTelegram)
}

func RedactConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return internalchannel.CloneConfigMap(raw)
	}
	out := internalchannel.CloneConfigMap(raw)
	if cfg.Token != "" {
		out["token"] = "***"
	}
	return out
}

func validateConfig(cfg pkgchannel.TelegramConfig) string {
	if cfg.Token == "" {
		return "telegram: missing token"
	}
	return ""
}
