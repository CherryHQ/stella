package qq

import "github.com/CherryHQ/stella/pkg/channel"

// handleModelCommand processes /model with optional arguments.
func (b *Bot) handleModelCommand(args string, reply func(string)) {
	channel.HandleModelCommand(channel.ModelCommandHandler{
		Args:        args,
		Reply:       reply,
		ListModels:  b.handler.ListModels,
		SwitchModel: b.handler.SwitchModel,
		OnSwitched: func(selected channel.ModelOption) {
			logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
		},
	})
}

// handleAgentCommand processes /agent with optional arguments.
func (b *Bot) handleAgentCommand(incoming channel.IncomingMessage, args string, reply func(string)) {
	channel.HandleAgentCommand(channel.AgentCommandHandler{
		Incoming:    incoming,
		Args:        args,
		Reply:       reply,
		ListAgents:  b.handler.ListAgents,
		SwitchAgent: b.handler.SwitchAgent,
		OnSwitched: func(agentID string) {
			logger().Info("agent switched", "agent_id", agentID)
		},
	})
}
