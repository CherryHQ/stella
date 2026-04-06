package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
)

const maxReviewMessages = 200

// BuildReviewContext implements memory.ReviewSource.
func (p *Provider) BuildReviewContext(ctx context.Context, session memory.Session, since time.Time) (string, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, session.ID)
	if err == sql.ErrNoRows {
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

// MarkReviewed implements memory.ReviewSource.
func (p *Provider) MarkReviewed(ctx context.Context, session memory.Session, at time.Time) error {
	conv, err := p.q.GetConversationBySessionID(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	watermark := at.UTC().Format("2006-01-02 15:04:05")
	return p.q.MarkConversationReviewedAt(ctx, sqlc.MarkConversationReviewedAtParams{
		SelfImproveReviewedAt: sql.NullString{String: watermark, Valid: true},
		ID:                    conv.ID,
	})
}

// ListUnreviewed implements memory.ReviewSource.
func (p *Provider) ListUnreviewed(ctx context.Context, agentID string, limit int) ([]memory.ReviewCandidate, error) {
	if limit <= 0 {
		limit = 10
	}

	convs, err := p.q.ListUnreviewedConversations(ctx, sqlc.ListUnreviewedConversationsParams{
		AgentID: sql.NullString{String: agentID, Valid: true},
		Limit:   int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list unreviewed: %w", err)
	}

	candidates := make([]memory.ReviewCandidate, 0, len(convs))
	for _, conv := range convs {
		candidate := memory.ReviewCandidate{
			Session: memory.Session{
				ID:      conv.SessionID,
				AgentID: agentID,
				Channel: conv.Channel,
			},
			LastActive: parseTime(conv.LastActive),
		}
		if conv.AgentID.Valid {
			candidate.Session.AgentID = conv.AgentID.String
		}
		if conv.UserID.Valid {
			candidate.Session.UserID = conv.UserID.Int64
		}
		if conv.SelfImproveReviewedAt.Valid {
			candidate.LastReviewedAt = parseTime(conv.SelfImproveReviewedAt.String)
		}
		candidates = append(candidates, candidate)
	}

	return candidates, nil
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
