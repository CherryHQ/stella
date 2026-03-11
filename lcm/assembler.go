package lcm

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/agent/runner"
)

// Assembler builds context for the model within a token budget.
type Assembler struct {
	q *Queries
}

// NewAssembler creates a new context assembler.
func NewAssembler(q *Queries) *Assembler {
	return &Assembler{q: q}
}

// Assemble builds a context window from context_items, respecting the token budget.
// It protects the freshTail most recent raw messages from being excluded.
// Returns RPCEvents for compatibility with the existing runner pipeline.
func (a *Assembler) Assemble(ctx context.Context, convID int64, budget int, freshTail int) ([]runner.RPCEvent, error) {
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
	var result []runner.RPCEvent
	tailTokens := 0
	tailEvents, err := a.resolveItems(ctx, tail)
	if err != nil {
		return nil, fmt.Errorf("resolve tail: %w", err)
	}
	for _, te := range tailEvents {
		tailTokens += EstimateTokens(eventText(te))
	}

	remaining := budget - tailTokens
	if remaining < 0 {
		remaining = 0
	}

	// Select older items that fit within remaining budget, newest first.
	var olderEvents []runner.RPCEvent
	for i := len(older) - 1; i >= 0; i-- {
		evts, err := a.resolveItem(ctx, older[i])
		if err != nil {
			return nil, fmt.Errorf("resolve item %d: %w", older[i].Ordinal, err)
		}
		tokens := 0
		for _, e := range evts {
			tokens += EstimateTokens(eventText(e))
		}
		if tokens > remaining {
			break // stop including older items
		}
		remaining -= tokens
		olderEvents = append(olderEvents, evts...)
	}

	// Reverse olderEvents (they were added newest-first).
	for i, j := 0, len(olderEvents)-1; i < j; i, j = i+1, j-1 {
		olderEvents[i], olderEvents[j] = olderEvents[j], olderEvents[i]
	}

	result = append(result, olderEvents...)
	result = append(result, tailEvents...)
	return result, nil
}

// splitFreshTail separates the last freshTail message-type items from the rest.
func splitFreshTail(items []ContextItem, freshTail int) (tail []ContextItem, older []ContextItem) {
	// Count message items from the end.
	msgCount := 0
	splitIdx := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].ItemType == ItemTypeMessage {
			msgCount++
			if msgCount >= freshTail {
				splitIdx = i
				break
			}
		}
	}
	return items[splitIdx:], items[:splitIdx]
}

// resolveItems resolves a slice of context items to RPCEvents.
func (a *Assembler) resolveItems(ctx context.Context, items []ContextItem) ([]runner.RPCEvent, error) {
	var result []runner.RPCEvent
	for _, item := range items {
		evts, err := a.resolveItem(ctx, item)
		if err != nil {
			return nil, err
		}
		result = append(result, evts...)
	}
	return result, nil
}

// resolveItem converts a single context item to RPCEvents.
func (a *Assembler) resolveItem(ctx context.Context, item ContextItem) ([]runner.RPCEvent, error) {
	switch item.ItemType {
	case ItemTypeMessage:
		if !item.MessageID.Valid {
			return nil, nil
		}
		msg, err := a.q.GetMessage(ctx, item.MessageID.Int64)
		if err != nil {
			return nil, fmt.Errorf("get message %d: %w", item.MessageID.Int64, err)
		}
		return messageToRPCEvents(msg), nil

	case ItemTypeSummary:
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
		return []runner.RPCEvent{summaryToRPCEvent(xml)}, nil

	default:
		return nil, nil
	}
}

// messageToRPCEvents converts a stored message to RPCEvents.
func messageToRPCEvents(msg Message) []runner.RPCEvent {
	switch msg.Role {
	case RoleUser:
		return []runner.RPCEvent{runner.UserMessageToRPCEvent(msg.Content)}
	case RoleAssistant:
		return []runner.RPCEvent{runner.AssistantMessageToRPCEvent(msg.Content)}
	case RoleTool:
		// Tool results are stored as a single message; reconstruct as tool_result event.
		return []runner.RPCEvent{{
			Type:   runner.RPCEventToolResult,
			Result: []byte(`"` + strings.ReplaceAll(msg.Content, `"`, `\"`) + `"`),
		}}
	default:
		return nil
	}
}

// summaryToRPCEvent creates a synthetic user message RPCEvent containing the summary XML.
func summaryToRPCEvent(xml string) runner.RPCEvent {
	return runner.UserMessageToRPCEvent(xml)
}

// FormatSummaryXML formats a summary as XML for model consumption.
func FormatSummaryXML(sum Summary, parents []Summary) string {
	var b strings.Builder

	fmt.Fprintf(&b, `<summary id="%s" kind="%s" depth="%d"`, sum.ID, sum.Kind, sum.Depth)
	if sum.EarliestAt.Valid {
		fmt.Fprintf(&b, ` earliest_at="%s"`, sum.EarliestAt.String)
	}
	if sum.LatestAt.Valid {
		fmt.Fprintf(&b, ` latest_at="%s"`, sum.LatestAt.String)
	}
	if sum.Kind == KindCondensed {
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

// eventText extracts the text content from an RPCEvent for token estimation.
func eventText(evt runner.RPCEvent) string {
	if evt.Summary != "" {
		return evt.Summary
	}
	if len(evt.Result) > 0 {
		return string(evt.Result)
	}
	return ""
}

// ContextStats returns token statistics for the current context.
func (a *Assembler) ContextStats(ctx context.Context, convID int64) (totalTokens int64, itemCount int64, err error) {
	totalTokens, err = a.q.GetContextTokenCount(ctx, convID)
	if err != nil {
		return 0, 0, err
	}
	itemCount, err = a.q.GetContextItemCount(ctx, convID)
	if err != nil {
		return 0, 0, err
	}
	return totalTokens, itemCount, nil
}

// NeedsCompaction checks if the context exceeds the budget threshold.
func (a *Assembler) NeedsCompaction(ctx context.Context, convID int64, budget int, threshold float64) bool {
	tokens, err := a.q.GetContextTokenCount(ctx, convID)
	if err != nil {
		return false
	}
	return float64(tokens) > float64(budget)*threshold
}

// FreshTailBoundary returns the ordinal that marks the start of the fresh tail.
func (a *Assembler) FreshTailBoundary(ctx context.Context, convID int64, freshTail int) (int64, error) {
	ids, err := a.q.GetFreshTailMessageIDs(ctx, GetFreshTailMessageIDsParams{
		ConversationID: convID,
		Limit:          int64(freshTail),
	})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// The last ID in the result is the oldest message in the fresh tail.
	// We need its context ordinal. For now return the message_id — the compaction
	// engine will use context items directly.
	return ids[len(ids)-1].Int64, nil
}
