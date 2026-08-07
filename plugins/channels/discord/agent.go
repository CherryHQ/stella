package discord

import (
	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// handleAgentCommand serves the text-based /agent flow: list agents, or switch
// by slug. Inline component pickers are a follow-up.
func (b *Bot) handleAgentCommand(m *discordgo.MessageCreate, args string) {
	msg := b.incomingMsg(m, nil)
	channel.HandleAgentCommand(channel.AgentCommandHandler{
		Ctx:         b.ctx,
		Incoming:    msg,
		Args:        args,
		Reply:       func(s string) { b.sendPlain(m.ChannelID, s) },
		ListAgents:  b.handler.ListAgents,
		SwitchAgent: b.handler.SwitchAgent,
		OnSwitched:  func(slug string) { logger().Info("agent switched", "agent_id", slug) },
	})
}
