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

const (
	groupWindowMaxRows   = 500
	groupWindowMaxTokens = 80_000
	groupWindowMaxBytes  = 4 << 20
	groupWindowPageSize  = 100

	// This is a global ceiling. Move to per-group budgets only when group traffic
	// or prompt-cache measurements show the shared limit is constraining them.
	groupWindowEvictionBlockTokens = 10_000
)

const groupHistoryOmittedMarker = "[system]: earlier group history omitted; use memory.search"

type groupWindowEvent struct {
	id               string
	seq              int64
	actorType        string
	actorID          string
	actorDisplayName string
	content          string
	line             string
	tokens           int
	bytes            int
}

// assembleGroup combines the canonical delivered event window with this
// agent's private LCM turns. Public group rows never enter ctx_message: only a
// turn's trigger anchor and its assistant/tool continuation are durable there.
func (p *Provider) assembleGroup(ctx context.Context, session memory.Session, budget, _ int) ([]ai.Message, error) {
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	private, err := p.loadGroupPrivateTurns(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("load private group turns: %w", err)
	}

	wake := memory.GroupWakeFromContext(ctx)
	window, omitted, err := p.loadGroupWindow(ctx, session.GroupID, session.AgentID, memory.GroupSeqFromContext(ctx), wake.MentionSeq, budget)
	if err != nil {
		return nil, err
	}

	inWindow := make(map[string]bool, len(window))
	for _, event := range window {
		inWindow[event.id] = true
	}

	messages := make([]ai.Message, 0, len(window)+1)
	// Head: this agent's own turns that the public window no longer covers.
	// Pre-migration turns have no origin and keep their old relative order; a
	// turn whose trigger was evicted keeps its stored anchor plus continuation —
	// the agent's private tool history must outlive the sliding public window.
	for _, turn := range private.turns {
		if turn.origin == "" || !inWindow[turn.origin] {
			messages = append(messages, turn.full...)
		}
	}
	if omitted {
		messages = append(messages, ai.UserMessage{Content: groupHistoryOmittedMarker})
	}
	for _, event := range window {
		// This agent's published reply is already represented by the private
		// continuation following its trigger, so never show it twice.
		if event.actorType == string(eventlog.ActorAgent) && event.actorID == session.AgentID {
			continue
		}
		messages = append(messages, ai.UserMessage{Content: event.line})
		messages = append(messages, private.byOrigin[event.id]...)
	}
	return sanitizeToolPairs(trimOldestCompleteTurns(messages, budget)), nil
}

type groupPrivateTurn struct {
	origin string // "" for pre-migration rows without a trigger anchor
	full   []ai.Message
}

type groupPrivateTurns struct {
	turns    []groupPrivateTurn
	byOrigin map[string][]ai.Message
}

func (p *Provider) loadGroupPrivateTurns(ctx context.Context, convID string) (groupPrivateTurns, error) {
	rows, err := p.q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		return groupPrivateTurns{}, err
	}
	out := groupPrivateTurns{byOrigin: make(map[string][]ai.Message)}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	parts, err := p.q.GetMessagePartsByMessages(ctx, ids)
	if err != nil {
		return groupPrivateTurns{}, err
	}
	partsByMessage := make(map[string][]loadedMessagePart, len(parts))
	for _, part := range parts {
		partsByMessage[part.MessageID] = append(partsByMessage[part.MessageID], part)
	}

	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && rows[end].Role != roleUser {
			end++
		}
		turnRows := rows[start:end]
		turnMessages := rowsToMessages(turnRows, partsByMessage)
		anchor := turnRows[0]
		if anchor.Role != roleUser || !anchor.OriginGroupMessageID.Valid {
			out.turns = append(out.turns, groupPrivateTurn{full: turnMessages})
		} else {
			// When the trigger is inside the public window, the canonical event
			// supplies the anchor and only the continuation follows it; when the
			// window has evicted it, the stored anchor keeps the turn coherent.
			origin := anchor.OriginGroupMessageID.String
			out.turns = append(out.turns, groupPrivateTurn{origin: origin, full: turnMessages})
			out.byOrigin[origin] = rowsToMessages(turnRows[1:], partsByMessage)
		}
		start = end
	}
	return out, nil
}

// loadGroupWindow reverse-pages only delivered rows strictly before triggerSeq.
// Each read has a LIMIT and collection stops under row, token, and byte ceilings.
func (p *Provider) loadGroupWindow(ctx context.Context, groupID, agentID string, triggerSeq, mentionSeq int64, budget int) ([]groupWindowEvent, bool, error) {
	if triggerSeq <= 0 {
		return nil, false, nil
	}
	maxTokens := groupWindowMaxTokens
	if budget > 0 && budget < maxTokens {
		maxTokens = budget
	}
	namer := eventlog.NewParticipantNamer(p.q)
	before := triggerSeq
	reverse := make([]groupWindowEvent, 0, min(groupWindowMaxRows, groupWindowPageSize))
	usedTokens, usedBytes := 0, 0
	for len(reverse) < groupWindowMaxRows {
		page, err := p.q.ListDeliveredGroupMessagesBeforeSeq(ctx, sqlc.ListDeliveredGroupMessagesBeforeSeqParams{
			GroupID: groupID, BeforeSeq: before, PageSize: groupWindowPageSize,
		})
		if err != nil {
			return nil, false, fmt.Errorf("list delivered group window: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			event := groupWindowEventFromRow(ctx, namer, groupID, row.ID, row.Seq, row.ActorType, row.ActorID, row.ActorDisplayName.String, row.Content)
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
			event := groupWindowEventFromRow(ctx, namer, groupID, mention.ID, mention.Seq, mention.ActorType, mention.ActorID, mention.ActorDisplayName.String, mention.Content)
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
			event := groupWindowEventFromRow(ctx, namer, groupID, mention.ID, mention.Seq, mention.ActorType, mention.ActorID, mention.ActorDisplayName.String, mention.Content)
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

func groupWindowEventFromRow(ctx context.Context, namer *eventlog.ParticipantNamer, groupID, id string, seq int64, actorType, actorID, displayName, content string) groupWindowEvent {
	name := displayName
	if name == "" {
		name = namer.Name(ctx, groupID, actorType, actorID)
	}
	line := fmt.Sprintf("[seq:%d %s]: %s", seq, eventlog.HandleDisplayName(name, actorType), content)
	message := ai.UserMessage{Content: line}
	return groupWindowEvent{
		id: id, seq: seq, actorType: actorType, actorID: actorID, actorDisplayName: displayName,
		content: content, line: line, tokens: estimateMessageTokens(message), bytes: len(line),
	}
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
