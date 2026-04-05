package qq

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/channel"
)

// modelList returns models optionally filtered by query.
func (b *Bot) modelList(query string) []channel.IndexedModel {
	models := b.handler.ListModels()
	if query == "" {
		return channel.IndexModels(models)
	}
	return channel.FilterModels(models, query)
}

// handleModelCommand processes /model with optional arguments.
// No args -> list models; text with "/" -> switch by name; text -> filter.
func (b *Bot) handleModelCommand(args string, reply func(string)) {
	query := channel.ParseModelArgs(args)

	// If the query looks like "provider/model", try switching directly.
	if query != "" && strings.Contains(query, "/") {
		b.switchModelByName(query, reply)
		return
	}

	models := b.modelList(query)
	if len(models) == 0 {
		if query != "" {
			reply(fmt.Sprintf("No models matching %q.", query))
		} else {
			reply("No models configured.")
		}
		return
	}
	reply(formatModelList(models, query))
}

// switchModelByName handles model switching by "provider/model" name.
func (b *Bot) switchModelByName(name string, reply func(string)) {
	name = strings.ToLower(strings.TrimSpace(name))
	models := b.handler.ListModels()
	var selected channel.ModelOption
	found := false
	for _, m := range models {
		if strings.ToLower(m.Provider+"/"+m.Model) == name {
			selected = m
			found = true
			break
		}
	}
	if !found {
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
	reply(formatAgentList(channel.IndexAgents(agents), currentAgentID))
}

// formatModelList builds a text-based model list.
func formatModelList(models []channel.IndexedModel, query string) string {
	var sb strings.Builder
	sb.WriteString("Available models")
	if query != "" {
		fmt.Fprintf(&sb, " (filter: %q)", query)
	}
	sb.WriteString(":\n\n")
	for _, m := range models {
		fmt.Fprintf(&sb, "• %s/%s\n", m.Provider, m.Model)
	}
	sb.WriteString("\nUse /model <provider/model> to switch.")
	return sb.String()
}

// formatAgentList builds a text-based agent list.
func formatAgentList(agents []channel.IndexedAgent, currentAgentID string) string {
	var sb strings.Builder
	sb.WriteString("Available agents:\n\n")
	for _, ag := range agents {
		prefix := "• "
		if ag.ID == currentAgentID {
			prefix = "✅ "
		}
		fmt.Fprintf(&sb, "%s%s (%s)\n", prefix, ag.ID, ag.Name)
	}
	sb.WriteString("\nUse /agent <slug> to switch.")
	return sb.String()
}
