package lcm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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

	// Resolve fresh tail — these are always included.
	tailMsgs, err := a.resolveItems(ctx, tail)
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
	var olderMsgs []ai.Message
	for i := len(older) - 1; i >= 0; i-- {
		msgs, err := a.resolveItem(ctx, older[i])
		if err != nil {
			return nil, fmt.Errorf("resolve item %d: %w", older[i].Ordinal, err)
		}
		tokens := 0
		for _, m := range msgs {
			tokens += estimateMessageTokens(m)
		}
		if tokens > remaining {
			break // stop including older items
		}
		remaining -= tokens
		olderMsgs = append(olderMsgs, msgs...)
	}

	// Reverse olderMsgs (they were added newest-first).
	for i, j := 0, len(olderMsgs)-1; i < j; i, j = i+1, j-1 {
		olderMsgs[i], olderMsgs[j] = olderMsgs[j], olderMsgs[i]
	}

	var result []ai.Message
	result = append(result, olderMsgs...)
	result = append(result, tailMsgs...)
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
	return items[splitIdx:], items[:splitIdx]
}

// resolveItems resolves a slice of context items to ai.Messages.
func (a *assembler) resolveItems(ctx context.Context, items []sqlc.CtxItem) ([]ai.Message, error) {
	var result []ai.Message
	for _, item := range items {
		msgs, err := a.resolveItem(ctx, item)
		if err != nil {
			return nil, err
		}
		result = append(result, msgs...)
	}
	return result, nil
}

// resolveItem converts a single context item to ai.Messages.
func (a *assembler) resolveItem(ctx context.Context, item sqlc.CtxItem) ([]ai.Message, error) {
	switch item.ItemType {
	case itemTypeMessage:
		if !item.MessageID.Valid {
			return nil, nil
		}
		msg, err := a.q.GetMessage(ctx, sqlc.GetMessageParams{ID: item.MessageID.String, ConversationID: item.ConversationID})
		if err != nil {
			return nil, fmt.Errorf("get message %s: %w", item.MessageID.String, err)
		}
		// Use rowsToMessages for single-row slices to get proper type reconstruction.
		return rowsToMessages([]sqlc.CtxMessage{msg}), nil

	case itemTypeSummary:
		if !item.SummaryID.Valid {
			return nil, nil
		}
		sum, err := a.q.GetSummary(ctx, sqlc.GetSummaryParams{ID: item.SummaryID.String, ConversationID: item.ConversationID})
		if err != nil {
			return nil, fmt.Errorf("get summary %s: %w", item.SummaryID.String, err)
		}
		parents, err := a.q.GetSummaryParents(ctx, sum.ID)
		if err != nil {
			return nil, fmt.Errorf("get summary parents %s: %w", sum.ID, err)
		}
		xml := FormatSummaryXML(sum, parents)
		return []ai.Message{ai.UserMessage{Content: xml}}, nil

	default:
		return nil, nil
	}
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
