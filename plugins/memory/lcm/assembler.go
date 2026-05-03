package lcm

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/db/sqlc"
)

// assembler builds context for the model within a token budget.
type assembler struct {
	q *sqlc.Queries
}

func newAssembler(q *sqlc.Queries) *assembler {
	return &assembler{q: q}
}

// assemble builds a context window from context_items, respecting the token budget.
// It protects the freshTail most recent raw messages from being excluded.
// Returns ai.Messages for direct use by the runner pipeline.
func (a *assembler) assemble(ctx context.Context, convID int64, budget int, freshTail int) ([]ai.Message, error) {
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
	tailMsgs = compactOversizedTailResults(tailMsgs)
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

// compactOversizedTailResults replaces large tool results that have already
// been processed (i.e. followed by at least one assistant message) with a compact
// placeholder. The returned slice is a shallow copy; original messages are not
// modified. ToolCallID, ToolName, IsError, and Timestamp are preserved.
func compactOversizedTailResults(msgs []ai.Message) []ai.Message {
	// Find the last AssistantMessage — tool results after this index are
	// part of the in-flight exchange and must not be compacted.
	lastAssistantIdx := -1
	for i, m := range msgs {
		if _, ok := m.(ai.AssistantMessage); ok {
			lastAssistantIdx = i
		}
	}
	if lastAssistantIdx < 0 {
		return msgs
	}

	result := make([]ai.Message, len(msgs))
	copy(result, msgs)

	for i := 0; i < lastAssistantIdx; i++ {
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
	}

	return result
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
		msg, err := a.q.GetMessage(ctx, item.MessageID.Int64)
		if err != nil {
			return nil, fmt.Errorf("get message %d: %w", item.MessageID.Int64, err)
		}
		// Use rowsToMessages for single-row slices to get proper type reconstruction.
		return rowsToMessages([]sqlc.CtxMessage{msg}), nil

	case itemTypeSummary:
		if !item.SummaryID.Valid {
			return nil, nil
		}
		sum, err := a.q.GetSummary(ctx, item.SummaryID.String)
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
