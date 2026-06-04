package lcm

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const defaultGroupFetchLimit = 200

// assembleGroup builds a context window from the group event log instead of
// per-session ctx_message/ctx_item. Messages from the current agent become
// AssistantMessages; everything else becomes UserMessages with actor attribution.
func (p *Provider) assembleGroup(ctx context.Context, groupID, agentID string, budget int) ([]ai.Message, error) {
	rows, err := p.q.ListRecentGroupMessages(ctx, sqlc.ListRecentGroupMessagesParams{
		GroupID:  groupID,
		MaxCount: defaultGroupFetchLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("list recent group messages: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// Rows come in descending seq order; reverse to chronological.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	msgs := groupRowsToMessages(rows, agentID)

	// Apply token budget: keep as many recent messages as fit, oldest dropped first.
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m)
	}
	cutIdx := 0
	for total > budget && cutIdx < len(msgs) {
		total -= estimateMessageTokens(msgs[cutIdx])
		cutIdx++
	}

	return msgs[cutIdx:], nil
}

// groupRowsToMessages converts event log rows to ai.Messages.
// The current agent's own messages become AssistantMessages;
// all other messages become UserMessages with actor attribution.
func groupRowsToMessages(rows []sqlc.CtxGroupMessage, selfAgentID string) []ai.Message {
	msgs := make([]ai.Message, 0, len(rows))
	for _, row := range rows {
		if row.Content == "" {
			continue
		}
		if row.ActorType == string(eventlog.ActorAgent) && row.ActorID == selfAgentID {
			msgs = append(msgs, ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: row.Content}},
			})
		} else {
			label := row.ActorID
			if row.ActorType == string(eventlog.ActorAgent) {
				label = "agent:" + row.ActorID
			}
			msgs = append(msgs, ai.UserMessage{
				Content: []ai.ContentBlock{ai.TextContent{
					Text: fmt.Sprintf("[%s]: %s", label, row.Content),
				}},
			})
		}
	}
	return msgs
}
