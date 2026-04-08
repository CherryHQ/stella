package channel

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgchannelruntime "github.com/vaayne/anna/pkg/channelruntime"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	feishuplugin "github.com/vaayne/anna/plugins/channels/feishu"
)

const (
	FeishuPluginID    = "channel/feishu"
	FeishuRuntimeName = "bot"
)

type FeishuRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   pkgplugins.NotificationRegistry
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(FeishuConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewFeishuManagedRuntime(deps FeishuRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return feishuplugin.New(feishuplugin.Config{
				AppID:             cfg.AppID,
				AppSecret:         cfg.AppSecret,
				EncryptKey:        cfg.EncryptKey,
				VerificationToken: cfg.VerificationToken,
				GroupMode:         cfg.GroupMode,
				Groups:            feishuGroupsToPluginConfig(cfg.Groups),
			}, handler)
		}
	}
	return pkgchannelruntime.NewBotManagedRuntime(pkgchannelruntime.BotRuntimeDeps[FeishuConfig]{
		Parent:               deps.Parent,
		Handler:              deps.Handler,
		Notifier:             deps.Notifier,
		Log:                  deps.Log,
		Now:                  deps.Now,
		Platform:             PlatformFeishu,
		DecodeConfig:         DecodeFeishuPluginConfig,
		ValidateConfig:       validateFeishuConfig,
		NotificationsEnabled: func(cfg FeishuConfig) bool { return cfg.EnableNotify },
		NewChannel:           deps.NewChannel,
		Snapshot:             feishuRuntimeSnapshot,
	})
}

func DecodeFeishuPluginConfig(raw map[string]any) (FeishuConfig, error) {
	return pkgchannel.DecodePluginConfig[FeishuConfig](raw, "feishu")
}

func RedactFeishuPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeFeishuPluginConfig(raw)
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

func FeishuPluginConfigSchema() map[string]any {
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

func validateFeishuConfig(cfg FeishuConfig) string {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return "feishu: missing app_id or app_secret"
	}
	return ""
}

func feishuRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg FeishuConfig) pkgplugins.RuntimeSnapshot {
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

func feishuGroupsToPluginConfig(groups map[string]FeishuGroup) map[string]feishuplugin.GroupConfig {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]feishuplugin.GroupConfig, len(groups))
	for k, v := range groups {
		out[k] = feishuplugin.GroupConfig{
			GroupMode:    v.GroupMode,
			SystemPrompt: v.SystemPrompt,
			ToolAllow:    append([]string(nil), v.ToolAllow...),
			ToolDeny:     append([]string(nil), v.ToolDeny...),
		}
	}
	return out
}
