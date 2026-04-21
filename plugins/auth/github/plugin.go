package github

import (
	"fmt"
	"maps"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "auth/github"

type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func defaultConfig() map[string]any {
	return map[string]any{
		"client_id":     "",
		"client_secret": "",
	}
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"client_id": map[string]any{
				"type":        "string",
				"description": "GitHub OAuth app client ID.",
			},
			"client_secret": map[string]any{
				"type":        "string",
				"description": "GitHub OAuth app client secret.",
			},
		},
		"required": []any{"client_id", "client_secret"},
	}
}

func decodeConfig(raw map[string]any) (Config, error) {
	var cfg Config
	if v, ok := raw["client_id"]; ok {
		s, ok := v.(string)
		if !ok {
			return Config{}, fmt.Errorf("client_id: must be a string")
		}
		cfg.ClientID = s
	}
	if v, ok := raw["client_secret"]; ok {
		s, ok := v.(string)
		if !ok {
			return Config{}, fmt.Errorf("client_secret: must be a string")
		}
		cfg.ClientSecret = s
	}
	return cfg, nil
}

func validateConfig(raw map[string]any) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if cfg.ClientID == "" {
		return fmt.Errorf("client_id: required")
	}
	if cfg.ClientSecret == "" {
		return fmt.Errorf("client_secret: required")
	}
	return nil
}

func redactConfig(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	maps.Copy(out, raw)
	if _, ok := out["client_secret"]; ok {
		out["client_secret"] = "***"
	}
	return out
}

func isConfigured(raw map[string]any) bool {
	cfg, err := decodeConfig(raw)
	return err == nil && cfg.ClientID != "" && cfg.ClientSecret != ""
}

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "auth",
			Name:         "github",
			DisplayName:  "GitHub",
			Description:  "GitHub OAuth app credentials for device-flow authentication.",
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
