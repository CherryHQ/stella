package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type FeishuRuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(pkgchannel.FeishuConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

func NewFeishuManagedRuntime(deps FeishuRuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg pkgchannel.FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID:        cfg.InstanceID,
				AppID:             cfg.AppID,
				AppSecret:         cfg.AppSecret,
				EncryptKey:        cfg.EncryptKey,
				VerificationToken: cfg.VerificationToken,
				Groups:            groupsToPluginConfig(cfg.Groups),
				TenantKey:         cfg.TenantKey,
				AutoProvision:     cfg.AutoProvision,
				AllowedChatIDs:    cfg.AllowedChatIDs,
				AllowDM:           cfg.AllowDM,
				RequireMention:    cfg.RequireMention,
			}, handler)
		}
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.FeishuConfig]{
		Parent:          deps.Parent,
		Handler:         deps.Handler,
		Notifier:        deps.Notifications,
		Log:             deps.Log,
		Now:             deps.Now,
		Platform:        pkgchannel.PlatformFeishu,
		DecodeConfig:    DecodeConfig,
		ConfigureConfig: configureConfig,
		ValidateConfig:  validateConfig,
		NewChannel:      deps.NewChannel,
		Snapshot:        runtimeSnapshot,
	})
}

func configureConfig(cfg pkgchannel.FeishuConfig, desired pkgplugins.PluginState) pkgchannel.FeishuConfig {
	if cfg.InstanceID == "" {
		cfg.InstanceID = desired.ID
	}
	return cfg
}

func DecodeConfig(raw map[string]any) (pkgchannel.FeishuConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return pkgchannel.FeishuConfig{}, fmt.Errorf("encode feishu config: %w", err)
	}
	cfg, err := pkgchannel.DecodeFeishuConfig(data)
	if err != nil {
		return cfg, fmt.Errorf("decode feishu config: %w", err)
	}
	return cfg, nil
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
			"groups": map[string]any{
				"type": "object",
				"additionalProperties": map[string]any{
					"type": "object",
					"properties": map[string]any{
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
			"tenant_key": map[string]any{
				"type":        "string",
				"description": "Feishu tenant key (enterprise ID). Optional — auto-detected at startup via the Feishu tenant API. Set explicitly to override.",
			},
			"auto_provision": map[string]any{
				"type":        "boolean",
				"description": "Automatically create Stella accounts for members of the bot's Feishu tenant.",
				"default":     false,
			},
			"allowed_chat_ids":               map[string]any{"type": "string", "description": "Comma-separated Feishu chat IDs allowed to send group messages."},
			"allow_dm":                       map[string]any{"type": "boolean", "description": "Accept direct messages for linked-user chat and account linking.", "default": true},
			"allow_unlinked_dm":              map[string]any{"type": "boolean", "description": "Allow unlinked Feishu users to use the bound agent in restricted guest sessions.", "default": false},
			"guest_message_limit_per_minute": map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestMessageLimitPerMinute, "default": pkgchannel.DefaultGuestMessageLimitPerMinute},
			"guest_max_per_channel":          map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestMaxPerChannel, "default": pkgchannel.DefaultGuestMaxPerChannel},
			"guest_retention_days":           map[string]any{"type": "integer", "minimum": 1, "maximum": pkgchannel.MaxGuestRetentionDays, "default": pkgchannel.DefaultGuestRetentionDays},
			"require_mention":                map[string]any{"type": "boolean", "description": "Only process group messages that mention this bot.", "default": true},
		},
		"required": []any{"app_id", "app_secret"},
	}
}

func validateConfig(cfg pkgchannel.FeishuConfig) string {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return "feishu: missing app_id or app_secret"
	}
	return validateConfigValues(cfg)
}

func validateConfigValues(cfg pkgchannel.FeishuConfig) string {
	if cfg.GuestMessageLimitPerMinute < 1 || cfg.GuestMessageLimitPerMinute > pkgchannel.MaxGuestMessageLimitPerMinute {
		return fmt.Sprintf("guest message limit per minute must be between 1 and %d", pkgchannel.MaxGuestMessageLimitPerMinute)
	}
	if cfg.GuestMaxPerChannel < 1 || cfg.GuestMaxPerChannel > pkgchannel.MaxGuestMaxPerChannel {
		return fmt.Sprintf("guest maximum per channel must be between 1 and %d", pkgchannel.MaxGuestMaxPerChannel)
	}
	if cfg.GuestRetentionDays < 1 || cfg.GuestRetentionDays > pkgchannel.MaxGuestRetentionDays {
		return fmt.Sprintf("guest retention days must be between 1 and %d", pkgchannel.MaxGuestRetentionDays)
	}
	return ""
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg pkgchannel.FeishuConfig) pkgplugins.RuntimeStatus {
	return pkgplugins.RuntimeStatus{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"app_id":                 cfg.AppID,
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
			SystemPrompt: v.SystemPrompt,
			ToolAllow:    append([]string(nil), v.ToolAllow...),
			ToolDeny:     append([]string(nil), v.ToolDeny...),
		}
	}
	return out
}
