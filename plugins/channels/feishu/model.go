package feishu

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vaayne/anna/pkg/channel"
)

// handleModelCommand processes /model with optional arguments.
// No args -> list models; number -> switch by index; text -> filter.
func (b *Bot) handleModelCommand(args string, reply func(string)) {
	query := channel.ParseModelArgs(args)
	models := b.handler.ListModels()

	if query == "" {
		indexed := channel.IndexModels(models)
		reply(formatModelList(indexed, ""))
		return
	}

	// Check if it's a direct "provider/model" name.
	if strings.Contains(query, "/") {
		b.switchModelByName(query, reply)
		return
	}

	// Check if it's a 1-based index number.
	if idx, err := strconv.Atoi(query); err == nil {
		b.switchModelByIdx(idx, reply)
		return
	}

	// Text arg -> filter models by substring match.
	filtered := channel.FilterModels(models, query)
	if len(filtered) == 0 {
		reply(fmt.Sprintf("No models matching %q.", query))
		return
	}
	reply(formatModelList(filtered, query))
}

// switchModelByIdx handles model switching by 1-based index.
func (b *Bot) switchModelByIdx(idx int, reply func(string)) {
	models := b.handler.ListModels()
	if idx < 1 || idx > len(models) {
		reply(fmt.Sprintf("Invalid selection, use a number between 1 and %d.", len(models)))
		return
	}
	selected := models[idx-1]

	if err := b.handler.SwitchModel(selected.Provider, selected.Model); err != nil {
		reply(fmt.Sprintf("Error switching model: %v", err))
		return
	}
	logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
	reply(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}

// switchModelByName handles model switching by "provider/model" name.
func (b *Bot) switchModelByName(name string, reply func(string)) {
	selected, ok := channel.FindModelByName(b.handler.ListModels(), name)
	if !ok {
		reply(fmt.Sprintf("Unknown model %q, use /model to list available models.", name))
		return
	}

	if err := b.handler.SwitchModel(selected.Provider, selected.Model); err != nil {
		reply(fmt.Sprintf("Error switching model: %v", err))
		return
	}
	logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
	reply(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}

// handleAgentCommand processes /agent with optional arguments.
func (b *Bot) handleAgentCommand(incoming channel.IncomingMessage, args string, reply func(string)) {
	// Direct switch by slug.
	if args != "" {
		if err := b.handler.SwitchAgent(context.Background(), incoming, args); err != nil {
			reply(fmt.Sprintf("Error switching agent: %v", err))
			return
		}
		logger().Info("agent switched", "agent_id", args)
		reply(fmt.Sprintf("Switched to agent: %s", args))
		return
	}

	agents, currentAgentID, err := b.handler.ListAgents(context.Background(), incoming)
	if err != nil {
		reply(fmt.Sprintf("Error listing agents: %v", err))
		return
	}
	if len(agents) == 0 {
		reply("No agents available.")
		return
	}
	reply(channel.FormatAgentList(channel.IndexAgents(agents), currentAgentID))
}

// formatModelList builds a text-based model list with 1-based numbered entries.
func formatModelList(models []channel.IndexedModel, query string) string {
	var sb strings.Builder
	sb.WriteString("Available models")
	if query != "" {
		fmt.Fprintf(&sb, " (filter: %q)", query)
	}
	sb.WriteString(":\n\n")
	for _, m := range models {
		fmt.Fprintf(&sb, "%d. %s/%s\n", m.GlobalIdx, m.Provider, m.Model)
	}
	sb.WriteString("\nUse /model <number> to switch.")
	return sb.String()
}
