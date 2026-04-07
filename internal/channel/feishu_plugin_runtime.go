package channel

import (
	"context"
	"log/slog"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
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
	Notifier   *Dispatcher
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(FeishuConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

type feishuManagedRuntime = botManagedRuntime[FeishuConfig]

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
	return NewBotManagedRuntime(BotRuntimeDeps[FeishuConfig]{
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
