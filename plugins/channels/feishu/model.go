package feishu

import (
	"fmt"
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

	// Text arg -> filter models by substring match.
	filtered := channel.FilterModels(models, query)
	if len(filtered) == 0 {
		reply(fmt.Sprintf("No models matching %q.", query))
		return
	}
	reply(formatModelList(filtered, query))
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
