package feishu

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type FeishuRuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.NotificationRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(pkgchannel.FeishuConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewFeishuManagedRuntime(deps FeishuRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg pkgchannel.FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				AppID:             cfg.AppID,
				AppSecret:         cfg.AppSecret,
				EncryptKey:        cfg.EncryptKey,
				VerificationToken: cfg.VerificationToken,
				GroupMode:         cfg.GroupMode,
				Groups:            groupsToPluginConfig(cfg.Groups),
			}, handler)
		}
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.FeishuConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifications,
		Log:                  deps.Log,
		Now:                  deps.Now,
		Platform:             pkgchannel.PlatformFeishu,
		DecodeConfig:         DecodeConfig,
		ValidateConfig:       validateConfig,
		NotificationsEnabled: func(cfg pkgchannel.FeishuConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             runtimeSnapshot,
	})
}

func DecodeConfig(raw map[string]any) (pkgchannel.FeishuConfig, error) {
	return pkgchannel.DecodePluginConfig[pkgchannel.FeishuConfig](raw, "feishu")
}

func RedactConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return pkgchannel.CloneConfigMap(raw)
	}
	out := pkgchannel.CloneConfigMap(raw)
	if cfg.AppSecret != "" {
		out["app_secret"] = "***"
	}
	if cfg.EncryptKey != "" {
		out["encrypt_key"] = "***"
	}
	if cfg.VerificationToken != "" {
		out["verification_token"] = "***"
	}
	return out
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"app_id": map[string]any{
				"type":        "string",
				"description": "Feishu bot app ID.",
			},
			"app_secret": map[string]any{
				"type":        "string",
				"description": "Feishu bot app secret.",
			},
			"encrypt_key": map[string]any{
				"type":        "string",
				"description": "Optional Feishu event encryption key.",
			},
			"verification_token": map[string]any{
				"type":        "string",
				"description": "Optional Feishu verification token.",
			},
			"group_mode": map[string]any{
				"type":        "string",
				"enum":        []any{"", "mention", "always", "disabled"},
				"description": "Default group-chat handling mode.",
			},
			"groups": map[string]any{
				"type": "object",
				"additionalProperties": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"group_mode": map[string]any{
							"type":        "string",
							"enum":        []any{"", "mention", "always", "disabled"},
							"description": "Override group mode for this chat.",
						},
						"system_prompt": map[string]any{
							"type":        "string",
							"description": "Optional system prompt override for this chat.",
						},
						"tool_allow": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Reserved allowlist of tools for this chat.",
						},
						"tool_deny": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Reserved denylist of tools for this chat.",
						},
					},
				},
				"description": "Per-chat overrides keyed by Feishu chat ID.",
			},
			"enable_notify": map[string]any{
				"type":        "boolean",
				"description": "Whether scheduler and system notifications are delivered to Feishu.",
				"default":     false,
			},
		},
		"required": []any{"app_id", "app_secret"},
	}
}

func validateConfig(cfg pkgchannel.FeishuConfig) string {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return "feishu: missing app_id or app_secret"
	}
	return ""
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg pkgchannel.FeishuConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"app_id":                 cfg.AppID,
			"group_mode":             cfg.GroupMode,
			"notify_enabled":         cfg.EnableNotify,
			"group_count":            len(cfg.Groups),
			"has_encrypt_key":        cfg.EncryptKey != "",
			"has_verification_token": cfg.VerificationToken != "",
		},
	}
}

func groupsToPluginConfig(groups map[string]pkgchannel.FeishuGroup) map[string]GroupConfig {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]GroupConfig, len(groups))
	for k, v := range groups {
		out[k] = GroupConfig{
			GroupMode:    v.GroupMode,
			SystemPrompt: v.SystemPrompt,
			ToolAllow:    append([]string(nil), v.ToolAllow...),
			ToolDeny:     append([]string(nil), v.ToolDeny...),
		}
	}
	return out
}
