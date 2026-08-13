package lcm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	maxFreshTailItems    = 120
	tailTokenCapFraction = 0.4
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
// It protects the freshTail most recent user turns from being excluded, subject to a tail token cap.
// Returns ai.Messages for direct use by the runner pipeline.
func (a *assembler) assemble(ctx context.Context, convID string, budget int, freshTail int) ([]ai.Message, error) {
	items, messages, summaries, children, partsByMessage, err := a.loadContextWindow(ctx, convID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	// Separate fresh tail (last N user turns) from older items.
	splitIdx := splitFreshTailIndex(items, freshTail)
	tail := items[splitIdx:]
	older := items[:splitIdx]

	// Resolve fresh tail — these are always included unless the token cap pushes
	// whole oldest turns back into older budget competition below.
	tailMsgs, tailTokens, compacted, err := resolveTailFromCaches(tail, messages, summaries, children, partsByMessage)
	if err != nil {
		return nil, fmt.Errorf("resolve tail: %w", err)
	}
	for tailTokenCap := int(float64(budget) * tailTokenCapFraction); tailTokens > tailTokenCap && tailTurnCount(tail) > 1; {
		nextSplitIdx, ok := advancePastOldestTurn(items, splitIdx)
		// The pair correction inside advancePastOldestTurn can walk the split
		// point back; without forward progress this loop would never terminate.
		if !ok || nextSplitIdx <= splitIdx {
			break
		}
		splitIdx = nextSplitIdx
		tail = items[splitIdx:]
		older = items[:splitIdx]
		tailMsgs, tailTokens, compacted, err = resolveTailFromCaches(tail, messages, summaries, children, partsByMessage)
		if err != nil {
			return nil, fmt.Errorf("resolve tail: %w", err)
		}
	}

	// Telemetry: count turns and tool results in the final tail.
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

	itemsPerTurn := float64(len(tailMsgs)) / float64(max(userTurns, 1))
	a.log.Info("lcm tail telemetry",
		slog.Int("tail_items", len(tail)),
		slog.Int("tail_messages", len(tailMsgs)),
		slog.Int("user_turns", userTurns),
		slog.Float64("items_per_turn", itemsPerTurn),
		slog.Int("tool_results", toolResults),
		slog.Int("tool_results_compacted", compacted),
	)

	remaining := max(budget-tailTokens, 0)

	// Select older items that fit within remaining budget, newest first.
	olderMsgs, err := resolveOlderWithinBudget(older, messages, summaries, children, partsByMessage, remaining)
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

// splitFreshTail separates the last freshTail user turns from the rest.
// The split point is adjusted so it never lands inside a tool_call/tool_result pair.
func splitFreshTail(items []sqlc.CtxItem, freshTail int) (tail []sqlc.CtxItem, older []sqlc.CtxItem) {
	splitIdx := splitFreshTailIndex(items, freshTail)
	return items[splitIdx:], items[:splitIdx]
}

func splitFreshTailIndex(items []sqlc.CtxItem, freshTail int) int {
	if freshTail <= 0 {
		return correctToolPairSplit(items, len(items))
	}

	turnCount := 0
	splitIdx := 0
	found := false
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].ItemType == itemTypeMessage && items[i].Role == roleUser {
			turnCount++
			if turnCount >= freshTail {
				splitIdx = i
				found = true
				break
			}
		}
	}
	if !found {
		splitIdx = 0
	}
	if countMessageItems(items[splitIdx:]) > maxFreshTailItems {
		splitIdx = splitLastMessageItemsIndex(items, maxFreshTailItems)
	}
	return correctToolPairSplit(items, splitIdx)
}

func splitLastMessageItemsIndex(items []sqlc.CtxItem, limit int) int {
	if limit <= 0 {
		return len(items)
	}
	msgCount := 0
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].ItemType != itemTypeMessage {
			continue
		}
		msgCount++
		if msgCount >= limit {
			return i
		}
	}
	return 0
}

func correctToolPairSplit(items []sqlc.CtxItem, splitIdx int) int {
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
	return splitIdx
}

func countMessageItems(items []sqlc.CtxItem) int {
	count := 0
	for _, item := range items {
		if item.ItemType == itemTypeMessage {
			count++
		}
	}
	return count
}

func tailTurnCount(items []sqlc.CtxItem) int {
	count := 0
	for _, item := range items {
		if item.ItemType == itemTypeMessage && item.Role == roleUser {
			count++
		}
	}
	return count
}

func advancePastOldestTurn(items []sqlc.CtxItem, splitIdx int) (int, bool) {
	for i := splitIdx + 1; i < len(items); i++ {
		if items[i].ItemType == itemTypeMessage && items[i].Role == roleUser {
			return correctToolPairSplit(items, i), true
		}
	}
	return splitIdx, false
}

func resolveOlderWithinBudget(older []sqlc.CtxItem, messages map[string]sqlc.CtxMessage, summaries map[string]sqlc.CtxSummary, children map[string][]sqlc.CtxSummary, partsByMessage map[string][]loadedMessagePart, remaining int) ([]ai.Message, error) {
	var olderMsgs []ai.Message
	for i := len(older) - 1; i >= 0; i-- {
		start, end := contextItemGroupBounds(older, i, messages)
		msgs, err := resolveItemsFromCaches(older[start:end], messages, summaries, children, partsByMessage)
		if err != nil {
			return nil, fmt.Errorf("resolve item %d: %w", older[start].Ordinal, err)
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
		// olderMsgs is accumulated newest-first. Reverse every atomic group now
		// so the final whole-slice reversal restores its internal chronology.
		for j := len(msgs) - 1; j >= 0; j-- {
			olderMsgs = append(olderMsgs, msgs[j])
		}
		i = start
	}
	return olderMsgs, nil
}

// contextItemGroupBounds returns an atomic parallel tool turn containing index.
// Ordinary assistant rows remain singletons because adjacency is not a stable
// response boundary.
func contextItemGroupBounds(items []sqlc.CtxItem, index int, messages map[string]sqlc.CtxMessage) (int, int) {
	if index < 0 || index >= len(items) {
		return index, index + 1
	}
	assistantIndex := index
	if items[index].ItemType == itemTypeMessage && items[index].EventType == eventTypeToolResult {
		for assistantIndex >= 0 && items[assistantIndex].ItemType == itemTypeMessage && items[assistantIndex].EventType == eventTypeToolResult {
			assistantIndex--
		}
	}
	start, assistantEnd, ok := assistantItemRunBounds(items, assistantIndex)
	if !ok {
		return index, index + 1
	}
	resultEnd := assistantEnd
	for resultEnd < len(items) && items[resultEnd].ItemType == itemTypeMessage && items[resultEnd].EventType == eventTypeToolResult {
		resultEnd++
	}
	if index >= start && index < resultEnd && assistantItemRunOwnsImmediateResults(items, start, assistantEnd, messages) {
		return start, resultEnd
	}
	return index, index + 1
}

func isAssistantMessageItem(item sqlc.CtxItem) bool {
	return item.ItemType == itemTypeMessage && item.Role == roleAssistant
}

func isAssistantToolCallItem(item sqlc.CtxItem) bool {
	return isAssistantMessageItem(item) && item.EventType == eventTypeToolCall
}

func assistantItemRunBounds(items []sqlc.CtxItem, index int) (int, int, bool) {
	if index < 0 || index >= len(items) || !isAssistantMessageItem(items[index]) {
		return index, index, false
	}
	start := index
	for start > 0 && isAssistantMessageItem(items[start-1]) {
		start--
	}
	end := index + 1
	for end < len(items) && isAssistantMessageItem(items[end]) {
		end++
	}
	return start, end, true
}

func assistantItemRunOwnsImmediateResults(items []sqlc.CtxItem, start, end int, messages map[string]sqlc.CtxMessage) bool {
	callCounts := make(map[string]int)
	for _, item := range items[start:end] {
		if !isAssistantToolCallItem(item) || !item.MessageID.Valid {
			continue
		}
		row, ok := messages[item.MessageID.String]
		if !ok {
			return false
		}
		call, ok := decodeToolCall(row.Content)
		if !ok {
			return false
		}
		callCounts[call.ID]++
	}
	if len(callCounts) == 0 {
		return false
	}

	resultCounts := make(map[string]int)
	for end < len(items) && items[end].ItemType == itemTypeMessage && items[end].EventType == eventTypeToolResult {
		item := items[end]
		if !item.MessageID.Valid {
			return false
		}
		row, ok := messages[item.MessageID.String]
		if !ok {
			return false
		}
		var envelope toolResultEnvelope
		if err := json.Unmarshal([]byte(row.Content), &envelope); err != nil {
			return false
		}
		resultCounts[envelope.ID]++
		end++
	}
	// A crash or cancellation may persist only a subset of parallel results.
	// One unambiguous completed pair is enough to prove that these adjacent
	// assistant rows belong to the result run; sanitizeToolPairs later removes
	// incomplete and conflicting ID groups from the reconstructed turn.
	return hasUniqueCompletedToolPair(callCounts, resultCounts)
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

// resolveTailFromCaches resolves tail items and replaces oversized processed
// tool results with placeholders before costing, so the token cap and the
// remaining-budget computation both see what the model will actually receive.
func resolveTailFromCaches(items []sqlc.CtxItem, messages map[string]sqlc.CtxMessage, summaries map[string]sqlc.CtxSummary, children map[string][]sqlc.CtxSummary, partsByMessage map[string][]loadedMessagePart) ([]ai.Message, int, int, error) {
	msgs, err := resolveItemsFromCaches(items, messages, summaries, children, partsByMessage)
	if err != nil {
		return nil, 0, 0, err
	}
	msgs, compacted := compactOversizedTailResults(msgs)
	tokens := 0
	for _, msg := range msgs {
		tokens += estimateMessageTokens(msg)
	}
	return msgs, tokens, compacted, nil
}

// resolveItemsFromCaches resolves a slice of context items to ai.Messages.
func resolveItemsFromCaches(items []sqlc.CtxItem, messages map[string]sqlc.CtxMessage, summaries map[string]sqlc.CtxSummary, children map[string][]sqlc.CtxSummary, partsByMessage map[string][]loadedMessagePart) ([]ai.Message, error) {
	var result []ai.Message
	for i := 0; i < len(items); i++ {
		item := items[i]
		switch item.ItemType {
		case itemTypeMessage:
			if !item.MessageID.Valid {
				continue
			}
			if start, end, ok := assistantItemRunBounds(items, i); ok && start == i && assistantItemRunOwnsImmediateResults(items, start, end, messages) {
				// Reconstruct an assistant run only after its immediately following
				// results prove a unique one-to-one tool turn. Adjacency alone is not
				// a stable boundary for separately appended ordinary messages.
				rows := make([]sqlc.CtxMessage, 0, 1)
				for ; i < end; i++ {
					assistantItem := items[i]
					if !assistantItem.MessageID.Valid {
						continue
					}
					msg, ok := messages[assistantItem.MessageID.String]
					if !ok {
						return nil, fmt.Errorf("get message %s: %w", assistantItem.MessageID.String, pgx.ErrNoRows)
					}
					rows = append(rows, msg)
				}
				i--
				result = append(result, rowsToMessages(rows, partsByMessage)...)
				continue
			}
			msg, ok := messages[item.MessageID.String]
			if !ok {
				return nil, fmt.Errorf("get message %s: %w", item.MessageID.String, pgx.ErrNoRows)
			}
			result = append(result, rowsToMessages([]sqlc.CtxMessage{msg}, partsByMessage)...)
		case itemTypeSummary:
			if !item.SummaryID.Valid {
				continue
			}
			sum, ok := summaries[item.SummaryID.String]
			if !ok {
				return nil, fmt.Errorf("get summary %s: %w", item.SummaryID.String, pgx.ErrNoRows)
			}
			content := any(FormatSummaryXML(sum, children[sum.ID]))
			if sum.ContainsNonPrincipalInput {
				content = eventlog.RenderInput(content, eventlog.MessageActor{
					Type: eventlog.ActorAgent,
					ID:   "compacted-agent-input",
				})
			}
			result = append(result, ai.UserMessage{Content: content})
		}
	}
	return result, nil
}

func (a *assembler) loadContextWindow(ctx context.Context, convID string) ([]sqlc.CtxItem, map[string]sqlc.CtxMessage, map[string]sqlc.CtxSummary, map[string][]sqlc.CtxSummary, map[string][]loadedMessagePart, error) {
	rows, err := a.q.ListContextItemsPage(ctx, sqlc.ListContextItemsPageParams{
		ConversationID: convID,
		OffsetCount:    0,
		LimitCount:     -1,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("list context items page: %w", err)
	}

	items := make([]sqlc.CtxItem, 0, len(rows))
	messages := make(map[string]sqlc.CtxMessage)
	summaries := make(map[string]sqlc.CtxSummary)
	summaryIDs := make([]string, 0)
	seenSummaryIDs := make(map[string]struct{})
	for _, row := range rows {
		item := sqlc.CtxItem{
			ConversationID: convID,
			Ordinal:        row.Ordinal,
			ItemType:       row.ItemType,
			MessageID:      row.MessageID,
			SummaryID:      row.SummaryID,
			EventType:      row.EventType,
			Role:           row.Role,
		}
		items = append(items, item)

		if item.ItemType == itemTypeMessage && row.MessageID.Valid {
			messages[row.MessageID.String] = sqlc.CtxMessage{
				ID:              row.MessageID.String,
				ConversationID:  convID,
				Seq:             row.MessageSeq.Int64,
				Role:            row.MessageRole.String,
				EventType:       row.MessageEventType.String,
				Content:         row.MessageContent.String,
				TokenCount:      row.MessageTokenCount.Int64,
				CreatedAt:       row.MessageCreatedAt.Time.UTC(),
				ActorType:       row.MessageActorType.String,
				ActorID:         row.MessageActorID,
				SourceSessionID: row.MessageSourceSessionID,
			}
		}
		if item.ItemType == itemTypeSummary && row.SummaryID.Valid {
			id := row.SummaryID.String
			summaries[id] = sqlc.CtxSummary{
				ID:                        id,
				ConversationID:            convID,
				Kind:                      row.SummaryKind.String,
				Depth:                     row.SummaryDepth.Int64,
				Content:                   row.SummaryContent.String,
				TokenCount:                row.SummaryTokenCount.Int64,
				EarliestAt:                row.SummaryEarliestAt,
				LatestAt:                  row.SummaryLatestAt,
				DescendantCount:           row.SummaryDescendantCount.Int64,
				DescendantTokenCount:      row.SummaryDescendantTokenCount.Int64,
				SourceMessageTokenCount:   row.SummarySourceMessageTokenCount.Int64,
				ContainsNonPrincipalInput: row.SummaryContainsNonPrincipalInput.Bool,
				CreatedAt:                 row.SummaryCreatedAt.Time.UTC(),
			}
			if _, ok := seenSummaryIDs[id]; !ok {
				seenSummaryIDs[id] = struct{}{}
				summaryIDs = append(summaryIDs, id)
			}
		}
	}

	children := make(map[string][]sqlc.CtxSummary)
	if len(summaryIDs) > 0 {
		childRefs, err := a.q.ListSummaryParentsBySummaryIDs(ctx, summaryIDs)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("list summary children: %w", err)
		}
		for _, ref := range childRefs {
			children[ref.SummaryID] = append(children[ref.SummaryID], sqlc.CtxSummary{ID: ref.ParentSummaryID})
		}
	}
	messageRows := make([]sqlc.CtxMessage, 0, len(messages))
	for _, message := range messages {
		messageRows = append(messageRows, message)
	}
	partsByMessage, err := loadMessageParts(ctx, a.q, messageIDsThatCanHaveParts(messageRows))
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return items, messages, summaries, children, partsByMessage, nil
}

// sanitizeToolPairs is a defense-in-depth pass that enforces the provider tool
// protocol: one assistant turn contains every parallel call, followed immediately
// by the consecutive results for that turn. Orphan results are dropped rather
// than promoted to user input, and calls without an adjacent result are stripped.
// Memory assembly always runs before the next live user message is appended.
func sanitizeToolPairs(msgs []ai.Message) []ai.Message {
	msgs = mergeAssistantRunsWithImmediateResults(msgs)
	result := make([]ai.Message, 0, len(msgs))
	for i := 0; i < len(msgs); i++ {
		assistant, ok := msgs[i].(ai.AssistantMessage)
		if !ok {
			// A result outside the consecutive run owned by the immediately
			// preceding assistant turn is an orphan and remains untrusted.
			if _, orphan := msgs[i].(ai.ToolResultMessage); !orphan {
				result = append(result, msgs[i])
			}
			continue
		}

		callCounts, hasCalls := toolCallIDCounts(assistant)
		if !hasCalls {
			result = append(result, assistant)
			continue
		}

		j := i + 1
		for ; j < len(msgs); j++ {
			if _, isResult := msgs[j].(ai.ToolResultMessage); !isResult {
				break
			}
		}
		resultCounts := toolResultIDCounts(msgs[i+1 : j])
		matched := make(map[string]struct{}, len(callCounts))
		for id, count := range callCounts {
			// Empty or duplicate IDs are ambiguous and cannot be repaired by
			// choosing one payload. Drop that complete ID group deterministically.
			if id != "" && count == 1 && resultCounts[id] == 1 {
				matched[id] = struct{}{}
			}
		}

		assistant.Content = filterAssistantToolCalls(assistant.Content, matched)
		result = append(result, assistant)
		for _, message := range msgs[i+1 : j] {
			toolResult := message.(ai.ToolResultMessage)
			if _, keep := matched[toolResult.ToolCallID]; keep {
				result = append(result, toolResult)
			}
		}
		i = j - 1
	}
	return result
}

// mergeAssistantRunsWithImmediateResults restores assistant blocks persisted as
// separate rows when the following results prove at least one completed tool
// pair. The sanitizer then keeps only completed, unambiguous ID groups.
func mergeAssistantRunsWithImmediateResults(msgs []ai.Message) []ai.Message {
	result := make([]ai.Message, 0, len(msgs))
	for i := 0; i < len(msgs); {
		first, ok := msgs[i].(ai.AssistantMessage)
		if !ok {
			result = append(result, msgs[i])
			i++
			continue
		}

		end := i + 1
		combined := first
		for end < len(msgs) {
			next, adjacent := msgs[end].(ai.AssistantMessage)
			if !adjacent {
				break
			}
			combined.Content = append(combined.Content, next.Content...)
			end++
		}
		resultEnd := end
		for resultEnd < len(msgs) {
			if _, isResult := msgs[resultEnd].(ai.ToolResultMessage); !isResult {
				break
			}
			resultEnd++
		}
		callCounts, hasCalls := toolCallIDCounts(combined)
		if end == i+1 || !hasCalls || !hasUniqueCompletedToolPair(callCounts, toolResultIDCounts(msgs[end:resultEnd])) {
			result = append(result, msgs[i:end]...)
			i = end
			continue
		}
		result = append(result, combined)
		i = end
	}
	return result
}

func toolCallIDCounts(message ai.AssistantMessage) (map[string]int, bool) {
	counts := make(map[string]int)
	hasCalls := false
	for _, block := range message.Content {
		if call, ok := block.(ai.ToolCall); ok {
			hasCalls = true
			counts[call.ID]++
		}
	}
	return counts, hasCalls
}

func toolResultIDCounts(messages []ai.Message) map[string]int {
	counts := make(map[string]int)
	for _, message := range messages {
		if result, ok := message.(ai.ToolResultMessage); ok {
			counts[result.ToolCallID]++
		}
	}
	return counts
}

func hasUniqueCompletedToolPair(callCounts, resultCounts map[string]int) bool {
	for id, count := range callCounts {
		if id != "" && count == 1 && resultCounts[id] == 1 {
			return true
		}
	}
	return false
}

func filterAssistantToolCalls(content []ai.ContentBlock, matched map[string]struct{}) []ai.ContentBlock {
	filtered := make([]ai.ContentBlock, 0, len(content))
	for _, block := range content {
		if call, ok := block.(ai.ToolCall); ok {
			if _, found := matched[call.ID]; !found {
				continue
			}
		}
		filtered = append(filtered, block)
	}
	if len(filtered) == 0 {
		return []ai.ContentBlock{ai.TextContent{Text: "[tool calls compacted]"}}
	}
	return filtered
}

// FormatSummaryXML formats a summary as XML for model consumption.
func FormatSummaryXML(sum sqlc.CtxSummary, children []sqlc.CtxSummary) string {
	var b strings.Builder

	fmt.Fprintf(&b, `<summary id="%s" kind="%s" depth="%d"`, sum.ID, sum.Kind, sum.Depth)
	if sum.EarliestAt.Valid {
		fmt.Fprintf(&b, ` earliest_at="%s"`, sum.EarliestAt.Time.UTC().Format(time.RFC3339Nano))
	}
	if sum.LatestAt.Valid {
		fmt.Fprintf(&b, ` latest_at="%s"`, sum.LatestAt.Time.UTC().Format(time.RFC3339Nano))
	}
	if sum.Kind == kindCondensed {
		fmt.Fprintf(&b, ` descendant_count="%d"`, sum.DescendantCount)
	}
	b.WriteString(">\n")

	if len(children) > 0 {
		b.WriteString("  <children>\n")
		for _, child := range children {
			fmt.Fprintf(&b, "    <summary_ref id=\"%s\" />\n", child.ID)
		}
		b.WriteString("  </children>\n")
	}

	content := memory.NeutralizeTags(sum.Content, "content", "summary")
	b.WriteString("  <content>\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("  </content>\n")
	b.WriteString("</summary>")

	return b.String()
}
