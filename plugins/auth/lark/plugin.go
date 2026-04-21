package lark

import (
	"fmt"
	"maps"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "auth/lark"

type Config struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	Brand     string `json:"brand"`
}

func defaultConfig() map[string]any {
	return map[string]any{
		"app_id":     "",
		"app_secret": "",
		"brand":      "lark",
	}
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"app_id": map[string]any{
				"type":        "string",
				"description": "Lark/Feishu OAuth app ID.",
			},
			"app_secret": map[string]any{
				"type":        "string",
				"description": "Lark/Feishu OAuth app secret.",
			},
			"brand": map[string]any{
				"type":        "string",
				"enum":        []any{"lark", "feishu"},
				"description": "Platform brand: \"lark\" (international) or \"feishu\" (China).",
				"default":     "lark",
			},
		},
		"required": []any{"app_id", "app_secret", "brand"},
	}
}

func decodeConfig(raw map[string]any) (Config, error) {
	var cfg Config
	if v, ok := raw["app_id"]; ok {
		s, ok := v.(string)
		if !ok {
			return Config{}, fmt.Errorf("app_id: must be a string")
		}
		cfg.AppID = s
	}
	if v, ok := raw["app_secret"]; ok {
		s, ok := v.(string)
		if !ok {
			return Config{}, fmt.Errorf("app_secret: must be a string")
		}
		cfg.AppSecret = s
	}
	if v, ok := raw["brand"]; ok {
		s, ok := v.(string)
		if !ok {
			return Config{}, fmt.Errorf("brand: must be a string")
		}
		cfg.Brand = s
	}
	return cfg, nil
}

func validateConfig(raw map[string]any) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if cfg.AppID == "" {
		return fmt.Errorf("app_id: required")
	}
	if cfg.AppSecret == "" {
		return fmt.Errorf("app_secret: required")
	}
	switch cfg.Brand {
	case "lark", "feishu":
	case "":
		return fmt.Errorf("brand: required")
	default:
		return fmt.Errorf("brand: must be one of \"lark\" or \"feishu\"")
	}
	return nil
}

func redactConfig(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	maps.Copy(out, raw)
	if _, ok := out["app_secret"]; ok {
		out["app_secret"] = "***"
	}
	return out
}

func isConfigured(raw map[string]any) bool {
	cfg, err := decodeConfig(raw)
	return err == nil && cfg.AppID != "" && cfg.AppSecret != "" && cfg.Brand != ""
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "auth",
			Name:         "lark",
			DisplayName:  "Lark / Feishu",
			Description:  "Lark/Feishu OAuth app credentials for device-flow authentication.",
			AdminVisible: true,
			HasConfig:    true,
			Capabilities: []string{
				pkgplugins.CapabilityConfig,
			},
		})
		host.AddAdmin(pkgplugins.AdminSpec{
			PluginID:      PluginID,
			DefaultConfig: defaultConfig,
			Schema:        configSchema(),
			Validate:      validateConfig,
			Redact:        redactConfig,
		})
	}))
}

// Configured reports whether the plugin has all required credentials set.
func Configured(raw map[string]any) bool {
	return isConfigured(raw)
}
