package telegram

import (
	"fmt"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

const modelsPerPage = 8

// sendModelKeyboard sends a paginated inline keyboard with model selection buttons.
func (b *Bot) sendModelKeyboard(c tele.Context, models []channel.IndexedModel) error {
	return b.sendModelPage(c, models, 0, "", false)
}

// sendModelPage renders a single page of the model keyboard.
// query is preserved in nav button data so filtered pagination works across pages.
// If edit is true, it edits the existing message instead of sending a new one.
func (b *Bot) sendModelPage(c tele.Context, models []channel.IndexedModel, page int, query string, edit bool) error {
	if len(models) == 0 {
		return c.Send("No models configured.")
	}

	totalPages := (len(models) + modelsPerPage - 1) / modelsPerPage
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * modelsPerPage
	end := min(start+modelsPerPage, len(models))

	b.mu.RLock()
	active, hasActive := b.chatModels[c.Chat().ID]
	b.mu.RUnlock()
	markup := &tele.ReplyMarkup{}

	var rows []tele.Row
	for i := start; i < end; i++ {
		m := models[i]
		label := fmt.Sprintf("%s/%s", m.Provider, m.Model)
		if hasActive && m.Provider == active.Provider && m.Model == active.Model {
			label = "✅ " + label
		}
		btn := markup.Data(label, "model_select", fmt.Sprintf("%d", m.GlobalIdx))
		rows = append(rows, markup.Row(btn))
	}

	// Navigation row. Encode "page|query" so filtered pagination persists.
	if totalPages > 1 {
		pageData := func(p int) string {
			if query == "" {
				return fmt.Sprintf("%d", p)
			}
			return fmt.Sprintf("%d|%s", p, query)
		}
		var navBtns []tele.Btn
		if page > 0 {
			navBtns = append(navBtns, markup.Data("◀ Prev", "model_page", pageData(page-1)))
		}
		navBtns = append(navBtns, markup.Data(fmt.Sprintf("%d/%d", page+1, totalPages), "model_noop"))
		if page < totalPages-1 {
			navBtns = append(navBtns, markup.Data("Next ▶", "model_page", pageData(page+1)))
		}
		rows = append(rows, markup.Row(navBtns...))
	}

	markup.Inline(rows...)

	text := fmt.Sprintf("Select a model (%d total):", len(models))
	if query != "" {
		text = fmt.Sprintf("Select a model (filter: %q, %d matches):", query, len(models))
	}
	if edit {
		return c.Edit(text, markup)
	}
	return c.Send(text, markup)
}

// switchModelByName handles model switching by "provider/model" name.
func (b *Bot) switchModelByName(c tele.Context, name string) error {
	selected, ok := channel.FindModelByName(b.handler.ListModels(), name)
	if !ok {
		return c.Send(fmt.Sprintf("Unknown model %q, use /model to list available models.", name))
	}

	if err := b.handler.SwitchModel(selected.Provider, selected.Model); err != nil {
		return c.Send(fmt.Sprintf("Error switching model: %v", err))
	}
	b.mu.Lock()
	b.chatModels[c.Chat().ID] = selected
	b.mu.Unlock()
	logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
	return c.Send(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}

// switchModelByIdx handles model switching by 1-based index.
// Used internally by inline keyboard callbacks.
func (b *Bot) switchModelByIdx(c tele.Context, idx int) error {
	models := b.handler.ListModels()
	if idx < 1 || idx > len(models) {
		return c.Send(fmt.Sprintf("Invalid selection, use a number between 1 and %d.", len(models)))
	}
	selected := models[idx-1]

	if err := b.handler.SwitchModel(selected.Provider, selected.Model); err != nil {
		return c.Send(fmt.Sprintf("Error switching model: %v", err))
	}
	b.mu.Lock()
	b.chatModels[c.Chat().ID] = selected
	b.mu.Unlock()
	logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
	return c.Send(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}
