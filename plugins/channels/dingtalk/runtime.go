package dingtalk

import (
	"context"
	"log/slog"
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
	NewChannel    func(DingTalkConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
	WrapHandler   pkgplugins.HandlerWrapper
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.Runtime {
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg DingTalkConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
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
	return pkgplugins.NewBotManagedRuntime(pkgplugins.BotRuntimeDeps[DingTalkConfig]{
		Parent: deps.Parent, Handler: deps.Handler, Notifier: deps.Notifications, Log: deps.Log, Now: deps.Now,
		Platform: pkgchannel.PlatformDingTalk, DecodeConfig: DecodeConfig,
		ConfigureConfig: func(cfg DingTalkConfig, desired pkgplugins.PluginState) DingTalkConfig {
			if cfg.InstanceID == "" {
				cfg.InstanceID = desired.ID
			}
			return cfg
		},
		ValidateConfig: validateConfig, NewChannel: deps.NewChannel,
		WrapHandler: deps.WrapHandler,
		Snapshot: func(now time.Time, state pkgplugins.RuntimeState, message string, cfg DingTalkConfig) pkgplugins.RuntimeStatus {
			return pkgplugins.RuntimeStatus{State: state, Message: message, UpdatedAt: now, Metadata: map[string]any{"client_id": cfg.ClientID}}
		},
	})
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
