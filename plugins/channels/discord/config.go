package discord

import (
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// DecodeConfig decodes a raw channel config map into a DiscordConfig.
//
// MentionOnly defaults to true when the key is absent so a freshly created or
// programmatically written config never floods a server: guild ingress stays
// gated to @-mentions and whitelisted channels unless an explicit `false` opts
// into responding to everything.
func DecodeConfig(raw map[string]any) (pkgchannel.DiscordConfig, error) {
	cfg, err := pkgchannel.DecodePluginConfig[pkgchannel.DiscordConfig](raw, pkgchannel.PlatformDiscord)
	if err != nil {
		return cfg, err
	}
	if _, ok := raw["mention_only"]; !ok {
		cfg.MentionOnly = true
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

func validateConfig(cfg pkgchannel.DiscordConfig) string {
	if cfg.Token == "" {
		return "discord: missing token"
	}
	return ""
}
