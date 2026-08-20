package lcm

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func groupCursorPipeline(agentID string) string { return memory.GroupIngestPipeline(agentID) }

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

	// 2. Read watermark: last event log seq this agent incorporated. Fail the
	// turn before model execution rather than assemble from seq 0.
	watermark, err := p.getGroupCursor(ctx, groupID, pipeline)
	if err != nil {
		return nil, err
	}

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
		injected = groupRowsToMessages(ctx, eventlog.NewParticipantNamer(p.q), groupID, rows, agentID)
	}

	// A deferred group turn commits peer rows in the dispatcher's accept tx. The
	// sink receives the full set before trim because rows outside this prompt
	// window still have to be committed before its cursor can advance.
	if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
		sink.SetInjected(injected)
		agentHistory = append(agentHistory, injected...)
	} else {
		// Legacy callers retain the original inline append path exactly. In
		// particular, this defensive content dedup only belongs here: deferred
		// turns must preserve legitimately repeated peer messages.
		injected, err = p.filterAlreadyPersistedInjected(ctx, convID, injected)
		if err != nil {
			return nil, fmt.Errorf("filter persisted injected messages: %w", err)
		}
		if len(injected) > 0 {
			if err := p.Append(ctx, session, injected...); err != nil {
				return nil, fmt.Errorf("persist between-turn messages: %w", err)
			}
			agentHistory = append(agentHistory, injected...)
		}
	}

	// 4. Apply the post-injection budget by whole user turns. The ordinary
	// assembler has already made tool turns provider-safe; a second message-level
	// trim must not detach tool results or a final answer from their user turn.
	agentHistory = trimOldestCompleteTurns(agentHistory, budget)
	return sanitizeToolPairs(agentHistory), nil
}

func trimOldestCompleteTurns(messages []ai.Message, budget int) []ai.Message {
	total := 0
	for _, message := range messages {
		total += estimateMessageTokens(message)
	}
	for len(messages) > 0 && total > budget {
		end := len(messages)
		for i := 1; i < len(messages); i++ {
			if _, startsNextTurn := messages[i].(ai.UserMessage); startsNextTurn {
				end = i
				break
			}
		}
		for _, message := range messages[:end] {
			total -= estimateMessageTokens(message)
		}
		messages = messages[end:]
	}
	return messages
}

func (p *Provider) CommitGroupCursor(ctx context.Context, session memory.Session, triggerSeq int64) error {
	return p.commitGroupCursorWithQueries(ctx, p.q, session, triggerSeq)
}

func (p *Provider) commitGroupCursorWithQueries(ctx context.Context, q *sqlc.Queries, session memory.Session, triggerSeq int64) error {
	if session.GroupID == "" || session.AgentID == "" || triggerSeq <= 0 {
		return nil
	}
	groupID := session.GroupID
	pipeline := groupCursorPipeline(session.AgentID)
	watermark, err := p.getGroupCursorWithQueries(ctx, q, groupID, pipeline)
	if err != nil {
		return err
	}
	if triggerSeq <= watermark {
		return nil
	}
	if err := q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  groupID,
		Pipeline: pipeline,
		LastSeq:  triggerSeq,
	}); err != nil {
		return fmt.Errorf("update group cursor: %w", err)
	}
	return nil
}

func (p *Provider) filterAlreadyPersistedInjected(ctx context.Context, convID string, injected []ai.Message) ([]ai.Message, error) {
	if len(injected) == 0 {
		return injected, nil
	}
	contents := make([]string, 0, len(injected))
	for _, msg := range injected {
		um, ok := msg.(ai.UserMessage)
		if !ok {
			continue
		}
		contents = append(contents, flattenUserContent(um))
	}
	if len(contents) == 0 {
		return injected, nil
	}
	seen := make(map[string]struct{})
	for start := 0; start < len(contents); start += 500 {
		end := min(start+500, len(contents))
		existingRows, err := p.q.ListExistingUserMessageContent(ctx, sqlc.ListExistingUserMessageContentParams{
			ConversationID: convID,
			Contents:       contents[start:end],
		})
		if err != nil {
			return nil, err
		}
		for _, content := range existingRows {
			seen[content] = struct{}{}
		}
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
	return out, nil
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

// getGroupCursor returns the last-seen event log seq for this agent, or 0 when
// the agent has none yet. A read error is returned, never masked as 0: a
// transient failure treated as "no cursor" would replay the entire group
// history and re-persist it.
func (p *Provider) getGroupCursor(ctx context.Context, groupID, pipeline string) (int64, error) {
	return p.getGroupCursorWithQueries(ctx, p.q, groupID, pipeline)
}

func (p *Provider) getGroupCursorWithQueries(ctx context.Context, q *sqlc.Queries, groupID, pipeline string) (int64, error) {
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: pipeline,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read group cursor: %w", err)
	}
	return cursor.LastSeq, nil
}

// groupRowsToMessages converts event log rows to ai.Messages.
// The current agent's own messages are skipped (they're already in ctx_message).
// All other messages become UserMessages with actor attribution.
//
// Attribution is a name, never an id: the model has to recognise the same
// participant here, in its roster, and in the trigger of its own turn, and it
// has to be able to address them back. The namer is the one place that decides.
func groupRowsToMessages(ctx context.Context, namer *eventlog.ParticipantNamer, groupID string, rows []sqlc.ListGroupMessagesBetweenSeqsRow, selfAgentID string) []ai.Message {
	msgs := make([]ai.Message, 0, len(rows))
	for _, row := range rows {
		if row.Content == "" {
			continue
		}
		if row.ActorType == string(eventlog.ActorAgent) && row.ActorID == selfAgentID {
			continue
		}
		msgs = append(msgs, ai.UserMessage{Content: namer.Line(ctx, groupID, row.Seq, row.ActorType, row.ActorID, row.Content)})
	}
	return msgs
}
