package main

import (
	"fmt"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/plugins/channels/feishu"
	"github.com/vaayne/anna/plugins/channels/qq"
	"github.com/vaayne/anna/plugins/channels/telegram"
	"github.com/vaayne/anna/plugins/channels/weixin"
)

func buildChannel(
	name string,
	store config.Store,
	poolManager *agent.PoolManager,
	authStore auth.AuthStore,
	engine *auth.PolicyEngine,
	linkCodes *auth.LinkCodeStore,
	listFn channel.ModelListFunc,
	switchFn channel.ModelSwitchFunc,
) (channel.Channel, error) {
	switch name {
	case "telegram":
		cfg := channel.LoadConfig[channel.TelegramConfig](store, name)
		if cfg == nil || cfg.Token == "" {
			return nil, fmt.Errorf("telegram: missing channel config")
		}
		return telegram.New(telegram.Config{
			Token:     cfg.Token,
			ChannelID: cfg.ChannelID,
			GroupMode: cfg.GroupMode,
		}, poolManager, store, listFn, switchFn,
			telegram.WithAuth(authStore, engine, linkCodes),
		)

	case "qq":
		cfg := channel.LoadConfig[channel.QQConfig](store, name)
		if cfg == nil || cfg.AppID == "" || cfg.AppSecret == "" {
			return nil, fmt.Errorf("qq: missing channel config")
		}
		return qq.New(qq.Config{
			AppID:     cfg.AppID,
			AppSecret: cfg.AppSecret,
			GroupMode: cfg.GroupMode,
		}, poolManager, store, listFn, switchFn,
			qq.WithAuth(authStore, engine, linkCodes),
		)

	case "feishu":
		cfg := channel.LoadConfig[channel.FeishuConfig](store, name)
		if cfg == nil || cfg.AppID == "" || cfg.AppSecret == "" {
			return nil, fmt.Errorf("feishu: missing channel config")
		}
		groups := make(map[string]feishu.GroupConfig, len(cfg.Groups))
		for k, v := range cfg.Groups {
			groups[k] = feishu.GroupConfig{
				GroupMode:    v.GroupMode,
				SystemPrompt: v.SystemPrompt,
				ToolAllow:    v.ToolAllow,
				ToolDeny:     v.ToolDeny,
			}
		}
		return feishu.New(feishu.Config{
			AppID:             cfg.AppID,
			AppSecret:         cfg.AppSecret,
			EncryptKey:        cfg.EncryptKey,
			VerificationToken: cfg.VerificationToken,
			GroupMode:         cfg.GroupMode,
			Groups:            groups,
		}, poolManager, store, listFn, switchFn,
			feishu.WithAuth(authStore, engine, linkCodes),
		)

	case "weixin":
		cfg := channel.LoadConfig[channel.WeixinConfig](store, name)
		if cfg == nil || cfg.BotToken == "" {
			return nil, fmt.Errorf("weixin: missing channel config")
		}
		return weixin.New(weixin.Config{
			BotToken: cfg.BotToken,
			BaseURL:  cfg.BaseURL,
			BotID:    cfg.BotID,
			UserID:   cfg.UserID,
		}, poolManager, store, listFn, switchFn,
			weixin.WithAuth(authStore, engine, linkCodes),
		)

	default:
		return nil, fmt.Errorf("unknown channel: %s", name)
	}
}
