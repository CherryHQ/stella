package telegram

import (
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

func DecodeConfig(raw map[string]any) (pkgchannel.TelegramConfig, error) {
	return pkgchannel.DecodePluginConfig[pkgchannel.TelegramConfig](raw, pkgchannel.PlatformTelegram)
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
	if cfg.Token == "" {
		return "telegram: missing token"
	}
	return ""
}
