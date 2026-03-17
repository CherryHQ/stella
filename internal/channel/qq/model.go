package qq

import (
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/channel"
)

// handleModelCommand processes /model with optional arguments.
// No args → list models; text with "/" → switch by name; text → filter.
func (b *Bot) handleModelCommand(rc *channel.ResolvedChat, args string, reply func(string)) {
	query := channel.ParseModelArgs(args)

	// If the query looks like "provider/model", try switching directly.
	if query != "" && strings.Contains(query, "/") {
		b.switchModelByName(rc, query, reply)
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

// switchModelByName handles model switching by "provider/model" name.
func (b *Bot) switchModelByName(rc *channel.ResolvedChat, name string, reply func(string)) {
	name = strings.ToLower(strings.TrimSpace(name))
	models := b.listFn()
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

	if _, err := rc.RotateSession(); err != nil {
		reply(fmt.Sprintf("Error rotating session: %v", err))
		return
	}
	if b.switchFn != nil {
		if err := b.switchFn(selected.Provider, selected.Model); err != nil {
			reply(fmt.Sprintf("Error switching model: %v", err))
			return
		}
	}
	b.mu.Lock()
	b.chatModels[rc.SessionKey] = selected
	b.mu.Unlock()
	logger().Info("model switched", "key", rc.SessionKey, "provider", selected.Provider, "model", selected.Model)
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
