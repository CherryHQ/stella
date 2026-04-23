package gh

import (
	"context"
	"fmt"
	"maps"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "tool/gh"

type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url"`
}

const promptContent = `Use the GitHub CLI directly from Anna's managed PATH.

- Authentication comes from Anna's GitHub OAuth connection. Prefer ` + "`gh`" + ` over raw API calls when it already supports the task.
- Common commands: ` + "`gh auth status`" + `, ` + "`gh repo view`" + `, ` + "`gh pr view`" + `, ` + "`gh pr checkout`" + `, ` + "`gh run list`" + `, ` + "`gh run view --log`" + `.
- If auth fails mid-session, reconnect GitHub via Anna's OAuth flow and start a new session.`

func defaultConfig() map[string]any {
	return map[string]any{
		"client_id":     "",
		"client_secret": "",
		"redirect_url":  "",
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
			"redirect_url": map[string]any{
				"type":        "string",
				"description": "OAuth redirect URI registered in the GitHub app. Leave empty to use the server default: {your-server}/api/auth/profile/oauth/github/callback.",
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

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "tool",
			Name:         "gh",
			DisplayName:  "GitHub CLI",
			Description:  "GitHub CLI integration with OAuth-backed auth, session env, and prompt guidance.",
			AdminVisible: true,
			HasConfig:    true,
			Capabilities: []string{pkgplugins.CapabilityBinary, pkgplugins.CapabilityConfig, pkgplugins.CapabilityPrompt},
		})
		host.AddAdmin(pkgplugins.AdminSpec{
			PluginID:      PluginID,
			DefaultConfig: defaultConfig,
			Schema:        configSchema(),
			Validate:      validateConfig,
			Redact:        redactConfig,
		})
		host.AddBinary(pkgplugins.BinarySpec{
			PluginID: PluginID,
			Name:     "gh",
			Repo:     "cli/cli",
			Version:  "2.89.0",
			Embed:    true,
			AssetTemplates: map[string]pkgplugins.BinaryAsset{
				"darwin-amd64":  {File: "gh_{version}_macOS_amd64.zip"},
				"darwin-arm64":  {File: "gh_{version}_macOS_arm64.zip"},
				"linux-amd64":   {File: "gh_{version}_linux_amd64.tar.gz"},
				"linux-arm64":   {File: "gh_{version}_linux_arm64.tar.gz"},
				"windows-amd64": {File: "gh_{version}_windows_amd64.zip"},
				"windows-arm64": {File: "gh_{version}_windows_arm64.zip"},
			},
		})
		host.AddSessionEnv(pkgplugins.SessionEnvSpec{
			PluginID: PluginID,
			EnvVar:   "GH_TOKEN",
			Source:   pkgplugins.SessionEnvSourceGitHubToken,
		})
		host.AddSystemPrompt(pkgplugins.SystemPromptSpec{
			PluginID: PluginID,
			Name:     "gh",
			Build: func(_ context.Context, _ pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
				return pkgplugins.SystemPromptSection{Title: "GitHub CLI", Content: promptContent}, nil
			},
		})
	}))
}
