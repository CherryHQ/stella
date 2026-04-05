package main

import (
	"fmt"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/plugins/channels/feishu"
	"github.com/vaayne/anna/plugins/channels/qq"
	"github.com/vaayne/anna/plugins/channels/telegram"
	"github.com/vaayne/anna/plugins/channels/weixin"
)

func buildChannel(
	name string,
	handler pkgchannel.MessageHandler,
	store config.Store,
) (pkgchannel.Channel, error) {
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
		}, handler)

	case "qq":
		cfg := channel.LoadConfig[channel.QQConfig](store, name)
		if cfg == nil || cfg.AppID == "" || cfg.AppSecret == "" {
			return nil, fmt.Errorf("qq: missing channel config")
		}
		return qq.New(qq.Config{
			AppID:     cfg.AppID,
			AppSecret: cfg.AppSecret,
			GroupMode: cfg.GroupMode,
		}, handler)

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
		}, handler)

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
		}, handler)

	default:
		return nil, fmt.Errorf("unknown channel: %s", name)
	}
}
