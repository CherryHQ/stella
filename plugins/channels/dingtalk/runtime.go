package dingtalk

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

type RuntimeDeps struct {
	Parent        context.Context
	Handler       pkgchannel.Handler
	Notifications pkgplugins.ChannelRegistry
	Log           *slog.Logger
	Now           func() time.Time
	NewChannel    func(pkgchannel.DingTalkConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
	WrapHandler   pkgplugins.HandlerWrapper
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg pkgchannel.DingTalkConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return New(Config{
				InstanceID:     cfg.InstanceID,
				ClientID:       cfg.ClientID,
				ClientSecret:   cfg.ClientSecret,
				AllowGroup:     cfg.AllowGroup,
				AllowDM:        cfg.AllowDM,
				RequireMention: cfg.RequireMention,
			}, handler)
		}
	}
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[pkgchannel.DingTalkConfig]{
		Parent: deps.Parent, Handler: deps.Handler, Notifier: deps.Notifications, Log: deps.Log, Now: deps.Now,
		Platform: pkgchannel.PlatformDingTalk, DecodeConfig: DecodeConfig,
		ConfigureConfig: func(cfg pkgchannel.DingTalkConfig, desired pkgplugins.PluginState) pkgchannel.DingTalkConfig {
			if cfg.InstanceID == "" {
				cfg.InstanceID = desired.ID
			}
			return cfg
		},
		ValidateConfig: validateConfig, NewChannel: deps.NewChannel,
		WrapHandler: deps.WrapHandler,
		Snapshot: func(now time.Time, state pkgplugins.RuntimeState, message string, cfg pkgchannel.DingTalkConfig) pkgplugins.RuntimeStatus {
			return pkgplugins.RuntimeStatus{State: state, Message: message, UpdatedAt: now, Metadata: map[string]any{"client_id": cfg.ClientID}}
		},
	})
}

func DecodeConfig(raw map[string]any) (pkgchannel.DingTalkConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return pkgchannel.DingTalkConfig{}, fmt.Errorf("encode dingtalk config: %w", err)
	}
	cfg, err := pkgchannel.DecodeDingTalkConfig(data)
	if err != nil {
		return cfg, fmt.Errorf("decode dingtalk config: %w", err)
	}
	return cfg, nil
}

func RedactConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return pkgchannel.CloneConfigMap(raw)
	}
	out := pkgchannel.CloneConfigMap(raw)
	if cfg.ClientSecret != "" {
		out["client_secret"] = "***"
	}
	return out
}

func validateConfig(cfg pkgchannel.DingTalkConfig) string {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return "dingtalk: missing client_id or client_secret"
	}
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
