package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vaayne/anna/internal/channel"
	tele "gopkg.in/telebot.v4"
)

const agentsPerPage = 8

// sendAgentKeyboard sends a paginated inline keyboard with agent selection buttons.
func (b *Bot) sendAgentKeyboard(c tele.Context, agents []channel.IndexedAgent, currentAgentID string) error {
	return b.sendAgentPage(c, agents, 0, currentAgentID, false)
}

// sendAgentPage renders a single page of the agent keyboard.
func (b *Bot) sendAgentPage(c tele.Context, agents []channel.IndexedAgent, page int, currentAgentID string, edit bool) error {
	if len(agents) == 0 {
		return c.Send("No agents available.")
	}

	totalPages := (len(agents) + agentsPerPage - 1) / agentsPerPage
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * agentsPerPage
	end := start + agentsPerPage
	if end > len(agents) {
		end = len(agents)
	}

	markup := &tele.ReplyMarkup{}
	var rows []tele.Row
	for i := start; i < end; i++ {
		ag := agents[i]
		label := fmt.Sprintf("%s (%s)", ag.ID, ag.Name)
		if ag.ID == currentAgentID {
			label = "✅ " + label
		}
		btn := markup.Data(label, "agent_select", fmt.Sprintf("%d", ag.GlobalIdx))
		rows = append(rows, markup.Row(btn))
	}

	// Navigation row.
	if totalPages > 1 {
		var navBtns []tele.Btn
		if page > 0 {
			navBtns = append(navBtns, markup.Data("◀ Prev", "agent_page", strconv.Itoa(page-1)))
		}
		navBtns = append(navBtns, markup.Data(fmt.Sprintf("%d/%d", page+1, totalPages), "agent_noop"))
		if page < totalPages-1 {
			navBtns = append(navBtns, markup.Data("Next ▶", "agent_page", strconv.Itoa(page+1)))
		}
		rows = append(rows, markup.Row(navBtns...))
	}

	markup.Inline(rows...)

	text := fmt.Sprintf("Select an agent (%d total):", len(agents))
	if edit {
		return c.Edit(text, markup)
	}
	return c.Send(text, markup)
}

// switchAgentByIdx handles agent switching by 1-based index.
func (b *Bot) switchAgentByIdx(c tele.Context, idx int) error {
	rc, err := b.resolve(c)
	if err != nil {
		return c.Send(fmt.Sprintf("Error: %v", err))
	}

	ctx := context.Background()
	agents, err := b.agentCmd.List(ctx)
	if err != nil {
		return c.Send(fmt.Sprintf("Error listing agents: %v", err))
	}
	if idx < 1 || idx > len(agents) {
		return c.Send(fmt.Sprintf("Invalid selection, use a number between 1 and %d.", len(agents)))
	}
	selected := agents[idx-1]

	if err := b.agentCmd.Switch(ctx, rc.User, rc.ChatCtx, selected.ID); err != nil {
		return c.Send(fmt.Sprintf("Error switching agent: %v", err))
	}

	logger().Info("agent switched", "key", rc.SessionKey, "agent_id", selected.ID)
	return c.Send(fmt.Sprintf("Switched to agent: %s (%s)", selected.ID, selected.Name))
}

// switchAgentBySlug handles agent switching by slug name.
// Accepts a pre-resolved rc to avoid a redundant resolve call.
func (b *Bot) switchAgentBySlug(c tele.Context, rc *channel.ResolvedChat, slug string) error {
	slug = strings.TrimSpace(slug)

	if err := b.agentCmd.Switch(context.Background(), rc.User, rc.ChatCtx, slug); err != nil {
		return c.Send(fmt.Sprintf("Error switching agent: %v", err))
	}

	logger().Info("agent switched", "key", rc.SessionKey, "agent_id", slug)
	return c.Send(fmt.Sprintf("Switched to agent: %s", slug))
}
