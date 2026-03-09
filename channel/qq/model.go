package qq

import (
	"fmt"
	"strings"

	"github.com/vaayne/anna/channel"
)

// handleModelCommand processes /model with optional arguments.
// No args → list models; number → switch by index; text → filter.
func (b *Bot) handleModelCommand(args, ch string, reply func(string)) {
	idx, query := channel.ParseModelArgs(args)

	if idx > 0 {
		b.switchModelByIdx(idx, ch, reply)
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

// switchModelByIdx handles model switching by 1-based index using Commander.
func (b *Bot) switchModelByIdx(idx int, ch string, reply func(string)) {
	selected, err := b.cmd.ModelSwitch(ch, idx)
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

// formatModelList builds a text-based model list with numbered entries.
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
