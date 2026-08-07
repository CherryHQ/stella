package discord

import (
	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// handleModelCommand serves the text-based /model flow: list models, or switch
// by "provider/model" name. Inline component pickers are a follow-up.
func (b *Bot) handleModelCommand(m *discordgo.MessageCreate, args string) {
	channel.HandleModelCommand(channel.ModelCommandHandler{
		Args:        args,
		Reply:       func(s string) { b.sendPlain(m.ChannelID, s) },
		ListModels:  b.handler.ListModels,
		SwitchModel: b.handler.SwitchModel,
		OnSwitched: func(opt channel.ModelOption) {
			b.mu.Lock()
			b.chatModels[m.ChannelID] = opt
			b.mu.Unlock()
			logger().Info("model switched", "provider", opt.Provider, "model", opt.Model)
		},
	})
}
