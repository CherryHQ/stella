package larkcli

import (
	"fmt"
	"maps"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const PluginID = "tool/lark-cli"

// Bridge: app_id / app_secret / brand credentials will move to ManifestOAuthProvider config
// once the provider-credential schema is implemented. Until then the Go stub owns the
// AdminSpec so the admin UI can render the config form and redact secrets correctly.

func defaultConfig() map[string]any {
	return map[string]any{
		"app_id":       "",
		"app_secret":   "",
		"brand":        "feishu",
		"redirect_url": "",
	}
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"app_id": map[string]any{
				"type":        "string",
				"description": "Feishu/Lark OAuth app ID.",
			},
			"app_secret": map[string]any{
				"type":        "string",
				"description": "Feishu/Lark OAuth app secret.",
			},
			"brand": map[string]any{
				"type":        "string",
				"enum":        []any{"lark", "feishu"},
				"description": `Platform brand: "feishu" (China, default) or "lark" (international).`,
				"default":     "feishu",
			},
			"redirect_url": map[string]any{
				"type":        "string",
				"description": "OAuth redirect URI registered in the Feishu/Lark app. Leave empty to derive from Admin UI origin.",
			},
		},
		"required": []any{"app_id", "app_secret", "brand"},
	}
}

func validateConfig(raw map[string]any) error {
	appID, _ := raw["app_id"].(string)
	appSecret, _ := raw["app_secret"].(string)
	brand, _ := raw["brand"].(string)
	if appID == "" {
		return fmt.Errorf("app_id: required")
	}
	if appSecret == "" {
		return fmt.Errorf("app_secret: required")
	}
	switch brand {
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

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "tool",
			Name:         "lark-cli",
			DisplayName:  "Lark CLI",
			Description:  "Lark CLI integration with OAuth-backed auth, bundled skills, session env, and prompt guidance.",
			AdminVisible: true,
			HasConfig:    true,
			Capabilities: []string{pkgplugins.CapabilityConfig},
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
