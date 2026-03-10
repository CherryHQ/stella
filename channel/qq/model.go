package qq

import (
	"fmt"
	"strings"

	"github.com/vaayne/anna/channel"
)

// handleModelCommand processes /model with optional arguments.
// No args → list models; text with "/" → switch by name; text → filter.
func (b *Bot) handleModelCommand(args, ch string, reply func(string)) {
	query := channel.ParseModelArgs(args)

	// If the query looks like "provider/model", try switching directly.
	if query != "" && strings.Contains(query, "/") {
		b.switchModelByName(query, ch, reply)
		return
	}

	models := b.cmd.ModelList(query)
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

// switchModelByName handles model switching by "provider/model" name using Commander.
func (b *Bot) switchModelByName(name, ch string, reply func(string)) {
	selected, err := b.cmd.ModelSwitchByName(ch, name)
	if err != nil {
		reply(fmt.Sprintf("Error: %v", err))
		return
	}
	b.mu.Lock()
	b.chatModels[ch] = selected
	b.mu.Unlock()
	logger().Info("model switched", "channel", ch, "provider", selected.Provider, "model", selected.Model)
	reply(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
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
