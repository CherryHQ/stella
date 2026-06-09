package lcm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func groupCursorPipeline(agentID string) string { return "lcm:" + agentID }

// assembleGroup builds a hybrid context window for group sessions.
//
// Per-agent reasoning (tool calls, tool results, assistant responses) lives in
// ctx_message/ctx_item — the standard LCM path. Cross-agent conversation lives
// in the event log. This method merges both:
//
//  1. Standard LCM assembly from ctx_message (agent's own conversation history)
//  2. Event log messages from other participants between the last watermark and
//     the current triggering message's seq
//
// The watermark (stored in ctx_group_ingest_cursor, pipeline="lcm:<agentID>") tracks which
// event log seq this agent has already incorporated. The triggering message's seq
// comes from the context (set by group_dispatch) so it can be excluded from
// injection — it enters via the normal Append + live userMsg path in chat.go.
func (p *Provider) assembleGroup(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	groupID := session.GroupID
	agentID := session.AgentID
	triggerSeq := memory.GroupSeqFromContext(ctx)
	pipeline := groupCursorPipeline(agentID)

	// 1. Standard LCM assembly: agent's own conversation with tool use.
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	agentHistory, err := p.assembler.assemble(ctx, convID, budget, freshTail)
	if err != nil {
		return nil, fmt.Errorf("assemble agent history: %w", err)
	}

	// 2. Read watermark: last event log seq this agent incorporated.
	watermark := p.getGroupCursor(ctx, groupID, pipeline)

	// 3. Read between-turn messages from event log.
	var injected []ai.Message
	if triggerSeq > 0 && triggerSeq > watermark+1 {
		rows, err := p.q.ListGroupMessagesBetweenSeqs(ctx, sqlc.ListGroupMessagesBetweenSeqsParams{
			GroupID:   groupID,
			AfterSeq:  watermark,
			BeforeSeq: triggerSeq,
		})
		if err != nil {
			return nil, fmt.Errorf("list between-turn messages: %w", err)
		}
		injected = groupRowsToMessages(rows, agentID)
	}

	// 3.5. Persist injected messages before the live user turn so future
	// assemblies preserve event-log chronology. Cursor movement is intentionally
	// deferred to CommitGroupCursor after the chat succeeds.
	injected = filterAlreadyPersistedInjected(injected, agentHistory)
	if len(injected) > 0 {
		if err := p.Append(ctx, session, injected...); err != nil {
			return nil, fmt.Errorf("persist between-turn messages: %w", err)
		}
		agentHistory = append(agentHistory, injected...)
		injected = nil
	}

	// 4. Merge: agent history first, then injected between-turn messages.
	merged := make([]ai.Message, 0, len(agentHistory)+len(injected))
	merged = append(merged, agentHistory...)
	merged = append(merged, injected...)

	// 5. Apply token budget: trim oldest messages first.
	total := 0
	for _, m := range merged {
		total += estimateMessageTokens(m)
	}
	cutIdx := 0
	for total > budget && cutIdx < len(merged) {
		total -= estimateMessageTokens(merged[cutIdx])
		cutIdx++
	}

	return sanitizeToolPairs(merged[cutIdx:]), nil
}

func (p *Provider) CommitGroupCursor(ctx context.Context, session memory.Session, triggerSeq int64) error {
	if session.GroupID == "" || session.AgentID == "" || triggerSeq <= 0 {
		return nil
	}
	groupID := session.GroupID
	pipeline := groupCursorPipeline(session.AgentID)
	watermark := p.getGroupCursor(ctx, groupID, pipeline)
	if triggerSeq <= watermark {
		return nil
	}
	if err := p.q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  groupID,
		Pipeline: pipeline,
		LastSeq:  triggerSeq,
	}); err != nil {
		return fmt.Errorf("update group cursor: %w", err)
	}
	return nil
}

func filterAlreadyPersistedInjected(injected, history []ai.Message) []ai.Message {
	if len(injected) == 0 || len(history) == 0 {
		return injected
	}
	seen := make(map[string]struct{}, len(history))
	for _, msg := range history {
		um, ok := msg.(ai.UserMessage)
		if !ok {
			continue
		}
		seen[flattenUserContent(um)] = struct{}{}
	}
	out := injected[:0]
	for _, msg := range injected {
		um, ok := msg.(ai.UserMessage)
		if !ok {
			out = append(out, msg)
			continue
		}
		if _, ok := seen[flattenUserContent(um)]; ok {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func flattenUserContent(msg ai.UserMessage) string {
	switch c := msg.Content.(type) {
	case string:
		return c
	case []ai.ContentBlock:
		return ai.FlattenText(c)
	default:
		return fmt.Sprintf("%v", c)
	}
}

// getGroupCursor returns the last-seen event log seq for this agent, or 0.
func (p *Provider) getGroupCursor(ctx context.Context, groupID, pipeline string) int64 {
	cursor, err := p.q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: pipeline,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		p.log.Warn("failed to read group cursor", "group_id", groupID, "error", err)
		return 0
	}
	return cursor.LastSeq
}

// groupRowsToMessages converts event log rows to ai.Messages.
// The current agent's own messages are skipped (they're already in ctx_message).
// All other messages become UserMessages with actor attribution.
func groupRowsToMessages(rows []sqlc.CtxGroupMessage, selfAgentID string) []ai.Message {
	msgs := make([]ai.Message, 0, len(rows))
	for _, row := range rows {
		if row.Content == "" {
			continue
		}
		if row.ActorType == string(eventlog.ActorAgent) && row.ActorID == selfAgentID {
			continue
		}
		label := row.ActorID
		if row.ActorType == string(eventlog.ActorAgent) {
			label = "agent:" + row.ActorID
		}
		msgs = append(msgs, ai.UserMessage{
			Content: []ai.ContentBlock{ai.TextContent{
				Text: fmt.Sprintf("[seq:%d %s]: %s", row.Seq, label, row.Content),
			}},
		})
	}
	return msgs
}
