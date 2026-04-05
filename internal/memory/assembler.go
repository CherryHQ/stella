package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/pkg/ai"
)

// Assembler builds context for the model within a token budget.
type Assembler struct {
	q *sqlc.Queries
}

// NewAssembler creates a new context assembler.
func NewAssembler(q *sqlc.Queries) *Assembler {
	return &Assembler{q: q}
}

// Assemble builds a context window from context_items, respecting the token budget.
// It protects the freshTail most recent raw messages from being excluded.
// Returns ai.Messages for direct use by the engine/runner pipeline.
func (a *Assembler) Assemble(ctx context.Context, convID int64, budget int, freshTail int) ([]ai.Message, error) {
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
	tailTokens := 0
	for _, m := range tailMsgs {
		tailTokens += estimateMessageTokens(m)
	}

	remaining := budget - tailTokens
	if remaining < 0 {
		remaining = 0
	}

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

// splitFreshTail separates the last freshTail message-type items from the rest.
func splitFreshTail(items []sqlc.CtxItem, freshTail int) (tail []sqlc.CtxItem, older []sqlc.CtxItem) {
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

// resolveItems resolves a slice of context items to ai.Messages.
func (a *Assembler) resolveItems(ctx context.Context, items []sqlc.CtxItem) ([]ai.Message, error) {
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
func (a *Assembler) resolveItem(ctx context.Context, item sqlc.CtxItem) ([]ai.Message, error) {
	switch item.ItemType {
	case ItemTypeMessage:
		if !item.MessageID.Valid {
			return nil, nil
		}
		msg, err := a.q.GetMessage(ctx, item.MessageID.Int64)
		if err != nil {
			return nil, fmt.Errorf("get message %d: %w", item.MessageID.Int64, err)
		}
		// Use rowsToMessages for single-row slices to get proper type reconstruction.
		return rowsToMessages([]sqlc.CtxMessage{msg}), nil

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

// estimateMessageTokens returns a rough token count for an ai.Message.
func estimateMessageTokens(msg ai.Message) int {
	switch m := msg.(type) {
	case ai.UserMessage:
		switch c := m.Content.(type) {
		case string:
			return EstimateTokens(c)
		case []ai.ContentBlock:
			return EstimateTokens(ai.FlattenText(c))
		default:
			return EstimateTokens(fmt.Sprintf("%v", c))
		}
	case ai.AssistantMessage:
		total := 0
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				total += EstimateTokens(b.Text)
			case ai.ToolCall:
				total += EstimateTokens(b.Name)
				if b.Arguments != nil {
					data, _ := json.Marshal(b.Arguments)
					total += EstimateTokens(string(data))
				}
			}
		}
		return total
	case ai.ToolResultMessage:
		return EstimateTokens(ai.FlattenText(m.Content))
	default:
		return 0
	}
}
