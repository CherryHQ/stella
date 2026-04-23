package larkcli

import (
	"context"
	"fmt"
	"maps"

	"github.com/vaayne/anna/internal/builddeps"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "tool/lark-cli"

type Config struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	Brand       string `json:"brand"`
	RedirectURL string `json:"redirect_url"`
}

const promptContent = `Use ` + "`lark-cli`" + ` directly from Anna's managed PATH and injected runtime auth.

- Prefer the builtin ` + "`lark`" + ` skill before ad hoc command discovery; it aggregates the adapted CLI modules Anna ships.
- Anna injects ` + "`LARKSUITE_CLI_USER_ACCESS_TOKEN`" + `, ` + "`LARKSUITE_CLI_APP_ID`" + `, and ` + "`LARKSUITE_CLI_BRAND`" + ` per session. Do not run ` + "`lark-cli auth login`" + ` or ` + "`lark-cli config init`" + ` unless the user explicitly wants a standalone local setup outside Anna.
- If auth or scope checks fail, use Anna's OAuth tooling, then restart the session so fresh runtime env is injected.`

func defaultConfig() map[string]any {
	return map[string]any{
		"app_id":       "",
		"app_secret":   "",
		"brand":        "lark",
		"redirect_url": "",
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
			"redirect_url": map[string]any{
				"type":        "string",
				"description": "OAuth redirect URI registered in the Lark app. Leave empty to use the server default: {your-server}/api/auth/profile/oauth/lark/callback.",
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
	if v, ok := raw["redirect_url"]; ok {
		s, ok := v.(string)
		if !ok {
			return Config{}, fmt.Errorf("redirect_url: must be a string")
		}
		cfg.RedirectURL = s
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
			Capabilities: []string{pkgplugins.CapabilityConfig, pkgplugins.CapabilityPrompt},
		})
		host.AddAdmin(pkgplugins.AdminSpec{
			PluginID:      PluginID,
			DefaultConfig: defaultConfig,
			Schema:        configSchema(),
			Validate:      validateConfig,
			Redact:        redactConfig,
		})
		host.AddSessionEnv(pkgplugins.SessionEnvSpec{
			PluginID: PluginID,
			EnvVar:   "LARKSUITE_CLI_USER_ACCESS_TOKEN",
			Source:   pkgplugins.SessionEnvSourceLarkAccessToken,
		})
		host.AddSessionEnv(pkgplugins.SessionEnvSpec{
			PluginID: PluginID,
			EnvVar:   "LARKSUITE_CLI_APP_ID",
			Source:   pkgplugins.SessionEnvSourceLarkAppID,
		})
		host.AddSessionEnv(pkgplugins.SessionEnvSpec{
			PluginID: PluginID,
			EnvVar:   "LARKSUITE_CLI_BRAND",
			Source:   pkgplugins.SessionEnvSourceLarkBrand,
		})
		host.AddBundledSkill(pkgplugins.BundledSkillSpec{
			PluginID: PluginID,
			Name:     "lark",
			Sync:     builddeps.SyncLarkBundledSkill,
		})
		host.AddSystemPrompt(pkgplugins.SystemPromptSpec{
			PluginID: PluginID,
			Name:     "lark-cli",
			Build: func(_ context.Context, _ pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
				return pkgplugins.SystemPromptSection{Title: "Lark CLI", Content: promptContent}, nil
			},
		})
	}))
}
