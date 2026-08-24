package lcm

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/grouptranscript"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func groupCursorPipeline(agentID string) string { return memory.GroupIngestPipeline(agentID) }

const (
	groupWindowMaxRows   = 500
	groupWindowMaxTokens = 80_000
	groupWindowMaxBytes  = 4 << 20
	groupWindowPageSize  = 100

	// This is a global ceiling. Move to per-group budgets only when group traffic
	// or prompt-cache measurements show the shared limit is constraining them.
	groupWindowEvictionBlockTokens = 10_000
)

// groupHistoryOmittedMarker is the emitted line and the token-reservation
// input; building it through the renderer keeps the two from drifting apart.
var groupHistoryOmittedMarker = grouptranscript.RenderGroupSystemLine("earlier group history omitted; use memory.search")

type groupWindowEvent struct {
	seq     int64
	content string
	line    string
	tokens  int
	bytes   int
}

// assembleGroup reconstructs the group prompt from the canonical public event
// log. Private LCM turns remain durable for recovery and audit, but never enter
// a later model prompt; a stopped turn contributes only one private tool-name
// note so the agent does not silently forget an external side effect.
func (p *Provider) assembleGroup(ctx context.Context, session memory.Session, budget, _ int) ([]ai.Message, error) {
	note, err := p.unrepliedGroupToolNote(ctx, session.GroupID, session.AgentID)
	if err != nil {
		return nil, err
	}
	// Reserve the trusted metadata before collection. The omitted marker is
	// reserved even when it will not be needed, so discovering older history
	// never causes a second, cache-destroying trim after window planning.
	markerTokens := estimateMessageTokens(ai.UserMessage{Content: groupHistoryOmittedMarker})
	noteTokens := estimateMessageTokens(ai.UserMessage{Content: note})
	if budget > 0 && markerTokens+noteTokens > budget {
		// A caller's hard budget wins over an advisory note. The next accepted
		// turn clears it, and omitting it is safer than overflowing a model cap.
		note = ""
		noteTokens = 0
	}
	reservedTokens := markerTokens + noteTokens

	wake := memory.GroupWakeFromContext(ctx)
	window, omitted, err := p.loadGroupWindow(ctx, session.GroupID, session.AgentID, memory.GroupSeqFromContext(ctx), wake.MentionSeq, budget, reservedTokens)
	if err != nil {
		return nil, err
	}

	messages := make([]ai.Message, 0, len(window)+2)
	if omitted {
		messages = append(messages, ai.UserMessage{Content: groupHistoryOmittedMarker})
	}
	for _, event := range window {
		messages = append(messages, ai.UserMessage{Content: event.line})
	}
	if note != "" {
		messages = append(messages, ai.UserMessage{Content: note})
	}
	return messages, nil
}

func (p *Provider) unrepliedGroupToolNote(ctx context.Context, groupID, agentID string) (string, error) {
	rows, err := p.q.ListUnrepliedGroupToolCallContents(ctx, sqlc.ListUnrepliedGroupToolCallContentsParams{
		GroupID: groupID, AgentID: agentID,
	})
	if err != nil {
		return "", fmt.Errorf("list unreplied group tool calls: %w", err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		call, ok := decodeToolCall(row.Content)
		if !ok || call.Name == "" {
			continue
		}
		if _, ok := seen[call.Name]; ok {
			continue
		}
		seen[call.Name] = struct{}{}
		names = append(names, call.Name)
	}
	if len(names) == 0 {
		return "", nil
	}
	return grouptranscript.RenderGroupToolActivityNote(rows[0].TriggerSeq, names), nil
}

// loadGroupWindow reverse-pages the canonical public stream strictly before the
// trigger. A peer is visible only after delivery; this agent also sees its own
// pending and failed rows so its private view cannot contradict the group log.
func (p *Provider) loadGroupWindow(ctx context.Context, groupID, agentID string, triggerSeq, mentionSeq int64, budget, reservedTokens int) ([]groupWindowEvent, bool, error) {
	if triggerSeq <= 0 {
		return nil, false, nil
	}
	maxTokens := groupWindowMaxTokens
	if budget > 0 {
		maxTokens = min(budget-reservedTokens, maxTokens)
	}
	if maxTokens <= 0 {
		return nil, false, nil
	}
	namer := eventlog.NewParticipantNamer(p.q)
	before := triggerSeq
	reverse := make([]groupWindowEvent, 0, min(groupWindowMaxRows, groupWindowPageSize))
	usedTokens, usedBytes := 0, 0
	for len(reverse) < groupWindowMaxRows {
		page, err := p.q.ListDeliveredGroupMessagesBeforeSeq(ctx, sqlc.ListDeliveredGroupMessagesBeforeSeqParams{
			GroupID: groupID, AgentID: agentID, BeforeSeq: before, PageSize: groupWindowPageSize,
		})
		if err != nil {
			return nil, false, fmt.Errorf("list delivered group window: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			event := groupWindowEventFromRow(ctx, namer, groupID, agentID, row.Seq, row.ActorType, row.ActorID, row.ActorDisplayName.String, row.Content, row.DeliveryState)
			if event.content == "" {
				continue
			}
			if len(reverse) == groupWindowMaxRows || usedTokens+event.tokens > maxTokens || usedBytes+event.bytes > groupWindowMaxBytes {
				goto collected
			}
			reverse = append(reverse, event)
			usedTokens += event.tokens
			usedBytes += event.bytes
		}
		before = page[len(page)-1].Seq
		if len(page) < groupWindowPageSize {
			break
		}
	}

collected:
	window := reverseGroupWindow(reverse)
	if mentionSeq > 0 && (len(window) == 0 || mentionSeq < window[0].seq) {
		mention, err := p.q.GetDeliveredGroupMessageBySeq(ctx, sqlc.GetDeliveredGroupMessageBySeqParams{GroupID: groupID, Seq: mentionSeq})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// A stale wake can point at an event that was never delivered. It is
			// not peer-visible evidence, so the delivered-only invariant wins.
		case err != nil:
			return nil, false, fmt.Errorf("get waking mention: %w", err)
		default:
			event := groupWindowEventFromRow(ctx, namer, groupID, agentID, mention.Seq, mention.ActorType, mention.ActorID, mention.ActorDisplayName.String, mention.Content, "delivered")
			if event.content != "" {
				window = append([]groupWindowEvent{event}, window...)
			}
		}
	}
	window = quantizeGroupWindow(window, maxTokens)
	if mentionSeq > 0 && !groupWindowContainsSeq(window, mentionSeq) {
		mention, err := p.q.GetDeliveredGroupMessageBySeq(ctx, sqlc.GetDeliveredGroupMessageBySeqParams{GroupID: groupID, Seq: mentionSeq})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Delivery state is authoritative even for a stale mention wake.
		case err != nil:
			return nil, false, fmt.Errorf("restore waking mention: %w", err)
		default:
			event := groupWindowEventFromRow(ctx, namer, groupID, agentID, mention.Seq, mention.ActorType, mention.ActorID, mention.ActorDisplayName.String, mention.Content, "delivered")
			if event.content != "" {
				var fit bool
				window, fit = makeRoomForForcedGroupEvent(window, event, maxTokens)
				if !fit {
					return nil, false, fmt.Errorf("waking mention exceeds group context ceiling")
				}
				window = append([]groupWindowEvent{event}, window...)
			}
		}
	}
	if len(window) == 0 {
		return nil, false, nil
	}
	omitted, err := p.q.ExistsGroupMessageBeforeSeq(ctx, sqlc.ExistsGroupMessageBeforeSeqParams{GroupID: groupID, BeforeSeq: window[0].seq})
	if err != nil {
		return nil, false, fmt.Errorf("check omitted group history: %w", err)
	}
	return window, omitted, nil
}

func groupWindowEventFromRow(ctx context.Context, namer *eventlog.ParticipantNamer, groupID, selfAgentID string, seq int64, actorType, actorID, displayName, content, deliveryState string) groupWindowEvent {
	name := displayName
	if name == "" {
		name = namer.Name(ctx, groupID, actorType, actorID)
	}
	line := grouptranscript.RenderGroupTranscriptLine(grouptranscript.GroupTranscriptEvent{
		Seq: seq, ActorType: actorType, DisplayName: name, Content: content,
		You: actorType == string(eventlog.ActorAgent) && actorID == selfAgentID, DeliveryState: deliveryState,
	})
	message := ai.UserMessage{Content: line}
	return groupWindowEvent{seq: seq, content: content, line: line, tokens: estimateMessageTokens(message), bytes: len(line)}
}

// makeRoomForForcedGroupEvent preserves the three public-window ceilings when
// a coalesced waking mention must be restored below the normal floor. It drops
// whole head blocks from ordinary history; an oversized mention fails the turn
// rather than silently violating a memory ceiling.
func makeRoomForForcedGroupEvent(window []groupWindowEvent, forced groupWindowEvent, maxTokens int) ([]groupWindowEvent, bool) {
	if forced.tokens > maxTokens || forced.bytes > groupWindowMaxBytes {
		return nil, false
	}
	usedTokens, usedBytes := 0, 0
	for _, event := range window {
		usedTokens += event.tokens
		usedBytes += event.bytes
	}
	for len(window) > 0 && (len(window)+1 > groupWindowMaxRows || usedTokens+forced.tokens > maxTokens || usedBytes+forced.bytes > groupWindowMaxBytes) {
		blockTokens, end := 0, 0
		for end < len(window) && blockTokens < groupWindowEvictionBlockTokens {
			blockTokens += window[end].tokens
			usedBytes -= window[end].bytes
			end++
		}
		usedTokens -= blockTokens
		window = window[end:]
	}
	return window, len(window)+1 <= groupWindowMaxRows && usedTokens+forced.tokens <= maxTokens && usedBytes+forced.bytes <= groupWindowMaxBytes
}

func groupWindowContainsSeq(window []groupWindowEvent, seq int64) bool {
	for _, event := range window {
		if event.seq == seq {
			return true
		}
	}
	return false
}

func reverseGroupWindow(reverse []groupWindowEvent) []groupWindowEvent {
	window := make([]groupWindowEvent, len(reverse))
	for i := range reverse {
		window[len(reverse)-1-i] = reverse[i]
	}
	return window
}

// quantizeGroupWindow discards complete 10k-token head blocks, instead of one
// message at a time. That keeps the surviving prompt prefix stable between
// evictions and improves provider prompt-cache reuse.
func quantizeGroupWindow(window []groupWindowEvent, maxTokens int) []groupWindowEvent {
	if maxTokens < groupWindowEvictionBlockTokens {
		return window
	}
	total := 0
	for _, event := range window {
		total += event.tokens
	}
	stableBudget := maxTokens - groupWindowEvictionBlockTokens
	for len(window) > 0 && total > stableBudget {
		blockTokens, end := 0, 0
		for end < len(window) && blockTokens < groupWindowEvictionBlockTokens {
			blockTokens += window[end].tokens
			end++
		}
		if end == len(window) {
			// Keep the newest complete block. The collection caps already bounded
			// it, and an empty public context is less useful than one large event.
			return window
		}
		total -= blockTokens
		window = window[end:]
	}
	return window
}

// CommitGroupCursor advances the durable group cursor outside a dispatcher tx,
// so the write runs in its own AgentRun-guarded tx: a fenced run cannot move
// the watermark after losing its lease.
func (p *Provider) CommitGroupCursor(ctx context.Context, session memory.Session, triggerSeq int64) error {
	return agentrun.WriteTx(ctx, p.db, func(q *sqlc.Queries) error {
		return p.commitGroupCursorWithQueries(ctx, q, session, triggerSeq)
	})
}

func (p *Provider) commitGroupCursorWithQueries(ctx context.Context, q *sqlc.Queries, session memory.Session, triggerSeq int64) error {
	if session.GroupID == "" || session.AgentID == "" || triggerSeq <= 0 {
		return nil
	}
	watermark, err := p.getGroupCursorWithQueries(ctx, q, session.GroupID, groupCursorPipeline(session.AgentID))
	if err != nil {
		return err
	}
	if triggerSeq <= watermark {
		return nil
	}
	if err := q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID: session.GroupID, Pipeline: groupCursorPipeline(session.AgentID), LastSeq: triggerSeq,
	}); err != nil {
		return fmt.Errorf("update group cursor: %w", err)
	}
	return nil
}

// getGroupCursor returns last_completed_trigger_seq for this agent, or zero
// when it has never durably completed a group turn. Read failures are never
// treated as zero because that would turn a transient error into a history replay.
func (p *Provider) getGroupCursor(ctx context.Context, groupID, pipeline string) (int64, error) {
	return p.getGroupCursorWithQueries(ctx, p.q, groupID, pipeline)
}

func (p *Provider) getGroupCursorWithQueries(ctx context.Context, q *sqlc.Queries, groupID, pipeline string) (int64, error) {
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{GroupID: groupID, Pipeline: pipeline})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read group cursor: %w", err)
	}
	return cursor.LastSeq, nil
}
