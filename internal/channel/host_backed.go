package channel

const (
	TelegramPluginID    = "channel/telegram"
	TelegramRuntimeName = "bot"
	QQPluginID          = "channel/qq"
	FeishuPluginID      = "channel/feishu"
)

type HostBackedChannel struct {
	Name     string
	PluginID string
}

var HostBackedChannels = []HostBackedChannel{
	{Name: PlatformTelegram, PluginID: TelegramPluginID},
	{Name: PlatformQQ, PluginID: QQPluginID},
	{Name: PlatformFeishu, PluginID: FeishuPluginID},
	{Name: PlatformWeixin, PluginID: WeixinPluginID},
}

var hostBackedChannelPluginIDs = map[string]struct{}{
	TelegramPluginID: {},
	QQPluginID:       {},
	FeishuPluginID:   {},
	WeixinPluginID:   {},
}

func IsHostBackedPlugin(id string) bool {
	_, ok := hostBackedChannelPluginIDs[id]
	return ok
}
