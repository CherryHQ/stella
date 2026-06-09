package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const olderResolveBatchSize = 32

// assembler builds context for the model within a token budget.
type assembler struct {
	q   *sqlc.Queries
	log *slog.Logger
}

func newAssembler(q *sqlc.Queries, log *slog.Logger) *assembler {
	if log == nil {
		log = slog.Default()
	}
	return &assembler{q: q, log: log}
}

// assemble builds a context window from context_items, respecting the token budget.
// It protects the freshTail most recent raw messages from being excluded.
// Returns ai.Messages for direct use by the runner pipeline.
func (a *assembler) assemble(ctx context.Context, convID string, budget int, freshTail int) ([]ai.Message, error) {
	items, err := a.q.GetContextItems(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("get context items: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}

	// Separate fresh tail (last N message items) from older items.
	tail, older := splitFreshTail(items, freshTail)
	tailMessages, err := a.loadMessagesByItem(ctx, tail)
	if err != nil {
		return nil, err
	}
	tailSummaries, err := a.loadSummariesByItem(ctx, tail)
	if err != nil {
		return nil, err
	}

	// Resolve fresh tail — these are always included.
	tailMsgs, err := a.resolveItemsFromCaches(ctx, tail, tailMessages, tailSummaries)
	if err != nil {
		return nil, fmt.Errorf("resolve tail: %w", err)
	}

	// Telemetry: count turns and tool results before compaction.
	userTurns := 0
	toolResults := 0
	for _, m := range tailMsgs {
		if _, ok := m.(ai.UserMessage); ok {
			userTurns++
		}
		if _, ok := m.(ai.ToolResultMessage); ok {
			toolResults++
		}
	}

	var compacted int
	tailMsgs, compacted = compactOversizedTailResults(tailMsgs)

	itemsPerTurn := float64(len(tailMsgs)) / float64(max(userTurns, 1))
	a.log.Info("lcm tail telemetry",
		slog.Int("tail_items", len(tail)),
		slog.Int("tail_messages", len(tailMsgs)),
		slog.Int("user_turns", userTurns),
		slog.Float64("items_per_turn", itemsPerTurn),
		slog.Int("tool_results", toolResults),
		slog.Int("tool_results_compacted", compacted),
	)

	tailTokens := 0
	for _, m := range tailMsgs {
		tailTokens += estimateMessageTokens(m)
	}

	remaining := max(budget-tailTokens, 0)

	// Select older items that fit within remaining budget, newest first.
	olderMsgs, err := a.resolveOlderWithinBudget(ctx, older, remaining)
	if err != nil {
		return nil, err
	}

	// Reverse olderMsgs (they were added newest-first).
	for i, j := 0, len(olderMsgs)-1; i < j; i, j = i+1, j-1 {
		olderMsgs[i], olderMsgs[j] = olderMsgs[j], olderMsgs[i]
	}

	var result []ai.Message
	result = append(result, olderMsgs...)
	result = append(result, tailMsgs...)
	result = sanitizeToolPairs(result)
	return result, nil
}

// compactOversizedTailResults replaces large tool results from completed prior
// turns with a compact placeholder. Only tool results that appear before the
// last UserMessage are eligible — everything at or after it belongs to the
// current user turn and is preserved at full size, including intermediate tool
// results in multi-step tool chains. The returned slice is a shallow copy;
// original messages are not modified. ToolCallID, ToolName, IsError, and
// Timestamp are preserved. The second return value is the count of results
// that were replaced.
func compactOversizedTailResults(msgs []ai.Message) ([]ai.Message, int) {
	// Find the last UserMessage. Tool results after this index are part of the
	// current user turn — even if assistant messages follow, the model may still
	// need them to synthesize its final answer (multi-step tool chains).
	lastUserIdx := -1
	for i, m := range msgs {
		if _, ok := m.(ai.UserMessage); ok {
			lastUserIdx = i
		}
	}
	if lastUserIdx <= 0 {
		return msgs, 0
	}

	result := make([]ai.Message, len(msgs))
	copy(result, msgs)

	compacted := 0
	for i := 0; i < lastUserIdx; i++ {
		tr, ok := result[i].(ai.ToolResultMessage)
		if !ok {
			continue
		}
		if estimateMessageTokens(tr) <= oversizedToolResultTokens {
			continue
		}
		result[i] = ai.ToolResultMessage{
			ToolCallID: tr.ToolCallID,
			ToolName:   tr.ToolName,
			Content: []ai.ContentBlock{ai.TextContent{
				Text: "[Content omitted — this large tool result has already been processed. Re-invoke the tool if you need the data again.]",
			}},
			IsError:   tr.IsError,
			Timestamp: tr.Timestamp,
		}
		compacted++
	}

	return result, compacted
}

// splitFreshTail separates the last freshTail message-type items from the rest.
// The split point is adjusted so it never lands inside a tool_call/tool_result pair.
func splitFreshTail(items []sqlc.CtxItem, freshTail int) (tail []sqlc.CtxItem, older []sqlc.CtxItem) {
	// Count message items from the end.
	msgCount := 0
	splitIdx := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].ItemType == itemTypeMessage {
			msgCount++
			if msgCount >= freshTail {
				splitIdx = i
				break
			}
		}
	}
	// Pull tool_results at the split boundary into the tail so they stay
	// with their tool_calls (which are at lower ordinals, already in tail).
	// Conversely, if the last item in "older" is a tool_call whose result
	// is in the tail, pull it into the tail too.
	for splitIdx > 0 && splitIdx < len(items) &&
		items[splitIdx].ItemType == itemTypeMessage &&
		items[splitIdx].EventType == eventTypeToolResult {
		splitIdx--
	}
	for splitIdx > 0 && items[splitIdx-1].ItemType == itemTypeMessage &&
		items[splitIdx-1].EventType == eventTypeToolCall {
		splitIdx--
	}
	return items[splitIdx:], items[:splitIdx]
}

func (a *assembler) resolveOlderWithinBudget(ctx context.Context, older []sqlc.CtxItem, remaining int) ([]ai.Message, error) {
	var olderMsgs []ai.Message
	for end := len(older); end > 0; {
		start := max(end-olderResolveBatchSize, 0)
		batch := older[start:end]
		messages, err := a.loadMessagesByItem(ctx, batch)
		if err != nil {
			return nil, err
		}
		summaries, err := a.loadSummariesByItem(ctx, batch)
		if err != nil {
			return nil, err
		}
		for i := len(batch) - 1; i >= 0; i-- {
			item := batch[i]
			msgs, err := a.resolveItemsFromCaches(ctx, batch[i:i+1], messages, summaries)
			if err != nil {
				return nil, fmt.Errorf("resolve item %d: %w", item.Ordinal, err)
			}
			tokens := 0
			for _, m := range msgs {
				tokens += estimateMessageTokens(m)
			}
			if tokens > remaining {
				olderMsgs = stripTrailingOrphanResults(olderMsgs)
				return olderMsgs, nil
			}
			remaining -= tokens
			olderMsgs = append(olderMsgs, msgs...)
		}
		end = start
	}
	return olderMsgs, nil
}

// stripTrailingOrphanResults removes ToolResultMessages from the end of msgs
// that have no matching ToolCall earlier in the slice. Since olderMsgs is built
// newest-first (later reversed), trailing items are the most recent — which are
// tool_results whose tool_calls we failed to include due to budget exhaustion.
func stripTrailingOrphanResults(msgs []ai.Message) []ai.Message {
	callIDs := make(map[string]struct{})
	for _, m := range msgs {
		if am, ok := m.(ai.AssistantMessage); ok {
			for _, b := range am.Content {
				if tc, ok := b.(ai.ToolCall); ok {
					callIDs[tc.ID] = struct{}{}
				}
			}
		}
	}
	for len(msgs) > 0 {
		tr, ok := msgs[len(msgs)-1].(ai.ToolResultMessage)
		if !ok {
			break
		}
		if _, found := callIDs[tr.ToolCallID]; found {
			break
		}
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

// resolveItemsFromCaches resolves a slice of context items to ai.Messages.
func (a *assembler) resolveItemsFromCaches(ctx context.Context, items []sqlc.CtxItem, messages map[string]sqlc.CtxMessage, summaries map[string]sqlc.CtxSummary) ([]ai.Message, error) {
	var result []ai.Message
	for _, item := range items {
		switch item.ItemType {
		case itemTypeMessage:
			if !item.MessageID.Valid {
				continue
			}
			msg, ok := messages[item.MessageID.String]
			if !ok {
				return nil, fmt.Errorf("get message %s: %w", item.MessageID.String, sql.ErrNoRows)
			}
			// Preserve the pre-existing one-context-item-to-one-message reconstruction.
			result = append(result, rowsToMessages([]sqlc.CtxMessage{msg})...)
		case itemTypeSummary:
			if !item.SummaryID.Valid {
				continue
			}
			sum, ok := summaries[item.SummaryID.String]
			if !ok {
				return nil, fmt.Errorf("get summary %s: %w", item.SummaryID.String, sql.ErrNoRows)
			}
			parents, err := a.q.GetSummaryParents(ctx, sum.ID)
			if err != nil {
				return nil, fmt.Errorf("get summary parents %s: %w", sum.ID, err)
			}
			result = append(result, ai.UserMessage{Content: FormatSummaryXML(sum, parents)})
		}
	}
	return result, nil
}

func (a *assembler) loadMessagesByItem(ctx context.Context, items []sqlc.CtxItem) (map[string]sqlc.CtxMessage, error) {
	idsByConversation := make(map[string][]string)
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.ItemType != itemTypeMessage || !item.MessageID.Valid {
			continue
		}
		key := item.ConversationID + "\x00" + item.MessageID.String
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		idsByConversation[item.ConversationID] = append(idsByConversation[item.ConversationID], item.MessageID.String)
	}
	out := make(map[string]sqlc.CtxMessage)
	for convID, ids := range idsByConversation {
		rows, err := a.q.ListMessagesByIDs(ctx, sqlc.ListMessagesByIDsParams{ConversationID: convID, MessageIds: ids})
		if err != nil {
			return nil, fmt.Errorf("list messages: %w", err)
		}
		for _, row := range rows {
			out[row.ID] = row
		}
	}
	return out, nil
}

func (a *assembler) loadSummariesByItem(ctx context.Context, items []sqlc.CtxItem) (map[string]sqlc.CtxSummary, error) {
	idsByConversation := make(map[string][]string)
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.ItemType != itemTypeSummary || !item.SummaryID.Valid {
			continue
		}
		key := item.ConversationID + "\x00" + item.SummaryID.String
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		idsByConversation[item.ConversationID] = append(idsByConversation[item.ConversationID], item.SummaryID.String)
	}
	out := make(map[string]sqlc.CtxSummary)
	for convID, ids := range idsByConversation {
		rows, err := a.q.ListSummariesByIDs(ctx, sqlc.ListSummariesByIDsParams{ConversationID: convID, SummaryIds: ids})
		if err != nil {
			return nil, fmt.Errorf("list summaries: %w", err)
		}
		for _, row := range rows {
			out[row.ID] = row
		}
	}
	return out, nil
}

// sanitizeToolPairs is a defense-in-depth pass that ensures every ToolResultMessage
// has a preceding ToolCall with a matching ID. Orphan tool_results are converted to
// UserMessages to preserve context. Orphan tool_calls on non-final assistant messages
// are stripped.
func sanitizeToolPairs(msgs []ai.Message) []ai.Message {
	callIDs := make(map[string]struct{})
	for _, m := range msgs {
		am, ok := m.(ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, b := range am.Content {
			if tc, ok := b.(ai.ToolCall); ok {
				callIDs[tc.ID] = struct{}{}
			}
		}
	}

	result := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		tr, ok := m.(ai.ToolResultMessage)
		if !ok {
			result = append(result, m)
			continue
		}
		if _, found := callIDs[tr.ToolCallID]; found {
			result = append(result, m)
			continue
		}
		text := ai.FlattenText(tr.Content)
		if text == "" {
			continue
		}
		result = append(result, ai.UserMessage{
			Content:   fmt.Sprintf("[Previous tool result from %s]: %s", tr.ToolName, truncateUTF8(text, 500)),
			Timestamp: tr.Timestamp,
		})
	}

	resultIDs := make(map[string]struct{})
	for _, m := range result {
		if tr, ok := m.(ai.ToolResultMessage); ok {
			resultIDs[tr.ToolCallID] = struct{}{}
		}
	}
	lastAssistantIdx := -1
	for i := len(result) - 1; i >= 0; i-- {
		if _, ok := result[i].(ai.AssistantMessage); ok {
			lastAssistantIdx = i
			break
		}
	}
	for i, m := range result {
		am, ok := m.(ai.AssistantMessage)
		if !ok || i == lastAssistantIdx {
			continue
		}
		var filtered []ai.ContentBlock
		for _, b := range am.Content {
			if tc, isTC := b.(ai.ToolCall); isTC {
				if _, found := resultIDs[tc.ID]; !found {
					continue
				}
			}
			filtered = append(filtered, b)
		}
		if len(filtered) == 0 {
			filtered = []ai.ContentBlock{ai.TextContent{Text: "[tool calls compacted]"}}
		}
		result[i] = ai.AssistantMessage{Content: filtered}
	}

	return result
}

// FormatSummaryXML formats a summary as XML for model consumption.
func FormatSummaryXML(sum sqlc.CtxSummary, parents []sqlc.CtxSummary) string {
	var b strings.Builder

	fmt.Fprintf(&b, `<summary id="%s" kind="%s" depth="%d"`, sum.ID, sum.Kind, sum.Depth)
	if sum.EarliestAt.Valid {
		fmt.Fprintf(&b, ` earliest_at="%s"`, sum.EarliestAt.String)
	}
	if sum.LatestAt.Valid {
		fmt.Fprintf(&b, ` latest_at="%s"`, sum.LatestAt.String)
	}
	if sum.Kind == kindCondensed {
		fmt.Fprintf(&b, ` descendant_count="%d"`, sum.DescendantCount)
	}
	b.WriteString(">\n")

	if len(parents) > 0 {
		b.WriteString("  <parents>\n")
		for _, p := range parents {
			fmt.Fprintf(&b, "    <summary_ref id=\"%s\" />\n", p.ID)
		}
		b.WriteString("  </parents>\n")
	}

	b.WriteString("  <content>\n")
	b.WriteString(sum.Content)
	if !strings.HasSuffix(sum.Content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("  </content>\n")
	b.WriteString("</summary>")

	return b.String()
}
