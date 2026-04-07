package telegram

import internalchannel "github.com/vaayne/anna/internal/channel"

func DecodeConfig(raw map[string]any) (internalchannel.TelegramConfig, error) {
	return internalchannel.DecodePluginConfig[internalchannel.TelegramConfig](raw, internalchannel.PlatformTelegram)
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

func validateConfig(cfg internalchannel.TelegramConfig) string {
	if cfg.Token == "" {
		return "telegram: missing token"
	}
	return ""
}
