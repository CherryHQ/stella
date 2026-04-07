package admin

import internalchannel "github.com/vaayne/anna/internal/channel"

var hostBackedChannelPluginIDs = map[string]struct{}{
	internalchannel.TelegramPluginID: {},
	internalchannel.QQPluginID:       {},
	internalchannel.FeishuPluginID:   {},
	internalchannel.WeixinPluginID:   {},
}

func isHostBackedChannelPlugin(id string) bool {
	_, ok := hostBackedChannelPluginIDs[id]
	return ok
}
