package lcm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxReviewMessages = 200

// BuildReviewContext implements memory.Reviewer.
func (p *Provider) BuildReviewContext(ctx context.Context, session memory.Session, since time.Time) (string, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return "", err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get conversation: %w", err)
	}

	var b strings.Builder

	if !since.IsZero() {
		// Include summaries as prior context.
		summaries, err := p.q.GetSummariesByConversation(ctx, conv.ID)
		if err == nil && len(summaries) > 0 {
			b.WriteString("<prior_context>\n")
			for _, s := range summaries {
				b.WriteString(FormatSummaryXML(s, nil))
				b.WriteString("\n")
			}
			b.WriteString("</prior_context>\n\n")
		}

		// Only include messages since the watermark.
		msgs, err := p.q.GetMessagesSince(ctx, sqlc.GetMessagesSinceParams{
			ConversationID: conv.ID,
			CreatedAt:      since.UTC().Format("2006-01-02 15:04:05"),
		})
		if err != nil {
			return "", fmt.Errorf("get messages since: %w", err)
		}
		appendReviewMessages(&b, msgs)
	} else {
		// Include all messages.
		msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
		if err != nil {
			return "", fmt.Errorf("get messages: %w", err)
		}
		appendReviewMessages(&b, msgs)
	}

	return b.String(), nil
}

func appendReviewMessages(b *strings.Builder, msgs []sqlc.CtxMessage) {
	if len(msgs) == 0 {
		return
	}
	if len(msgs) > maxReviewMessages {
		msgs = msgs[len(msgs)-maxReviewMessages:]
	}
	for _, m := range msgs {
		fmt.Fprintf(b, "[%s] %s\n", m.Role, m.Content)
	}
}
