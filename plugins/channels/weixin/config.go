package weixin

import pkgchannel "github.com/CherryHQ/stella/pkg/channel"

type WeixinConfig struct {
	InstanceID   string `json:"-"`
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"base_url"`
	BotID        string `json:"bot_id"`
	UserID       string `json:"user_id"`
	SKRouteTag   string `json:"sk_route_tag"`
	EnableNotify bool   `json:"enable_notify"`
}

func DecodeConfig(raw map[string]any) (WeixinConfig, error) {
	return pkgchannel.DecodePluginConfig[WeixinConfig](raw, "weixin")
}
