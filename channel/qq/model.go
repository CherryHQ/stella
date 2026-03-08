package qq

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vaayne/anna/channel"
)

// ModelOption re-exports channel.ModelOption for use by callers.
type ModelOption = channel.ModelOption

// ModelListFunc re-exports channel.ModelListFunc for use by callers.
type ModelListFunc = channel.ModelListFunc

// ModelSwitchFunc re-exports channel.ModelSwitchFunc for use by callers.
type ModelSwitchFunc = channel.ModelSwitchFunc

// handleModelCommand processes /model with optional arguments.
// No args → list models; number → switch by index; text → filter.
func (b *Bot) handleModelCommand(args, ch string, reply func(string)) {
	models := b.listFn()

	if args == "" {
		reply(formatModelList(models, ""))
		return
	}

	// Numeric arg → direct switch by index.
	if idx, err := strconv.Atoi(args); err == nil {
		b.switchModel(models, idx, ch, reply)
		return
	}

	// Text arg → filter models by substring match.
	filtered := filterModels(models, args)
	if len(filtered) == 0 {
		reply(fmt.Sprintf("No models matching %q.", args))
		return
	}
	reply(formatModelList(filtered, args))
}

// switchModel handles model switching by 1-based index.
func (b *Bot) switchModel(models []ModelOption, idx int, ch string, reply func(string)) {
	if idx < 1 || idx > len(models) {
		reply(fmt.Sprintf("Invalid selection. Use a number between 1 and %d.", len(models)))
		return
	}
	selected := models[idx-1]
	if b.switchFn != nil {
		if err := b.switchFn(selected.Provider, selected.Model); err != nil {
			reply(fmt.Sprintf("Error switching model: %v", err))
			return
		}
	}
	if _, err := b.pool.RotateSession(ch); err != nil {
		logger().Error("rotate session after model switch failed", "channel", ch, "error", err)
	}
	b.mu.Lock()
	b.chatModels[ch] = selected
	b.mu.Unlock()
	logger().Info("model switched", "channel", ch, "provider", selected.Provider, "model", selected.Model)
	reply(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}

// formatModelList builds a text-based model list with numbered entries.
func formatModelList(models []ModelOption, query string) string {
	var sb strings.Builder
	sb.WriteString("Available models")
	if query != "" {
		fmt.Fprintf(&sb, " (filter: %q)", query)
	}
	sb.WriteString(":\n\n")
	for i, m := range models {
		fmt.Fprintf(&sb, "%d. %s/%s\n", i+1, m.Provider, m.Model)
	}
	sb.WriteString("\nUse /model <number> to switch.")
	return sb.String()
}

// filterModels returns models matching the query (case-insensitive substring).
func filterModels(models []ModelOption, query string) []ModelOption {
	if query == "" {
		return models
	}
	query = strings.ToLower(query)
	var out []ModelOption
	for _, m := range models {
		label := strings.ToLower(m.Provider + "/" + m.Model)
		if strings.Contains(label, query) {
			out = append(out, m)
		}
	}
	return out
}
