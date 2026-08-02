package lcm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxReviewTokens = 100_000

// BuildReviewContext implements memory.Reviewer.
func (p *Provider) BuildReviewContext(ctx context.Context, session memory.Session, since time.Time) (string, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return "", err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get conversation: %w", err)
	}

	var b strings.Builder

	if !since.IsZero() {
		remainingBudget := maxReviewTokens
		summaries, err := p.q.GetSummariesByConversation(ctx, conv.ID)
		if err == nil && len(summaries) > 0 {
			remainingBudget = appendReviewSummaries(&b, summaries, remainingBudget)
		}

		msgs, err := p.q.GetMessagesSince(ctx, sqlc.GetMessagesSinceParams{
			ConversationID: conv.ID,
			CreatedAt:      since.UTC(),
		})
		if err != nil {
			return "", fmt.Errorf("get messages since: %w", err)
		}
		partsByMessage, err := loadMessageParts(ctx, p.q, messageIDsThatCanHaveParts(msgs))
		if err != nil {
			return "", err
		}
		appendReviewMessagesWithParts(&b, msgs, partsByMessage, remainingBudget)
	} else {
		msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
		if err != nil {
			return "", fmt.Errorf("get messages: %w", err)
		}
		partsByMessage, err := loadMessageParts(ctx, p.q, messageIDsThatCanHaveParts(msgs))
		if err != nil {
			return "", err
		}
		appendReviewMessagesWithParts(&b, msgs, partsByMessage, maxReviewTokens)
	}

	return b.String(), nil
}

func appendReviewSummaries(b *strings.Builder, summaries []sqlc.CtxSummary, budget int) int {
	if budget <= 0 {
		return 0
	}
	xmlByIndex := make([]string, len(summaries))
	selected := make([]int, 0, len(summaries))
	remaining := budget
	for i := len(summaries) - 1; i >= 0; i-- {
		xml := FormatSummaryXML(summaries[i], nil)
		cost := memory.EstimateTokens(xml) + 4
		if remaining-cost < 0 {
			break
		}
		xmlByIndex[i] = xml
		selected = append(selected, i)
		remaining -= cost
	}
	if len(selected) == 0 {
		return budget
	}

	b.WriteString("<prior_context>\n")
	for i := len(selected) - 1; i >= 0; i-- {
		b.WriteString(xmlByIndex[selected[i]])
		b.WriteString("\n")
	}
	b.WriteString("</prior_context>\n\n")
	return remaining
}

func appendReviewMessages(b *strings.Builder, msgs []sqlc.CtxMessage) {
	appendReviewMessagesWithParts(b, msgs, nil, maxReviewTokens)
}

func appendReviewMessagesWithParts(b *strings.Builder, msgs []sqlc.CtxMessage, partsByMessage map[string][]loadedMessagePart, budget int) {
	if budget <= 0 {
		return
	}
	// Filter out tool results — they're large and useless for the reviewer.
	filtered := make([]sqlc.CtxMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != roleTool {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == 0 {
		return
	}

	// Take the tail that fits within the token budget.
	start := len(filtered)
	for i := len(filtered) - 1; i >= 0; i-- {
		cost := memory.EstimateTokens(reviewMessageText(filtered[i], partsByMessage[filtered[i].ID])) + memory.EstimateTokens(filtered[i].Role) + 4
		if budget-cost < 0 {
			break
		}
		budget -= cost
		start = i
	}

	for _, m := range filtered[start:] {
		fmt.Fprintf(b, "[%s] %s\n", m.Role, reviewMessageText(m, partsByMessage[m.ID]))
	}
}

func reviewMessageText(msg sqlc.CtxMessage, parts []loadedMessagePart) string {
	if len(parts) > 0 {
		return stablePartText(parts)
	}
	return msg.Content
}
