package lcm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const summarizeConcurrency = 4

// compactionEngine runs leaf and condensed compaction passes.
type compactionEngine struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	summarizer memory.Summarizer
	freshTail  int
}

func newCompactionEngine(db *pgxpool.Pool, q *sqlc.Queries, summarizer memory.Summarizer, freshTail int) *compactionEngine {
	if freshTail <= 0 {
		freshTail = defaultFreshTail
	}
	return &compactionEngine{
		db:         db,
		q:          q,
		summarizer: summarizer,
		freshTail:  freshTail,
	}
}

// compact runs compaction on a conversation based on the given mode.
func (c *compactionEngine) compact(ctx context.Context, convID string, mode memory.CompactionMode) (*memory.CompactionResult, error) {
	start := time.Now()
	result := &memory.CompactionResult{}

	tokensBefore, err := c.q.GetContextTokenCount(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("get token count: %w", err)
	}
	result.TokensBefore = int(tokensBefore)

	switch mode {
	case memory.CompactionIncremental:
		if err := c.runPasses(ctx, convID, result); err != nil {
			return nil, err
		}

	case memory.CompactionFull:
		// Repeat leaf+condensed passes until no more compaction is possible.
		for i := range 10 { // safety limit
			leafBefore := result.LeafSummariesCreated
			condensedBefore := result.CondensedSummariesCreated
			if err := c.runPasses(ctx, convID, result); err != nil {
				return nil, fmt.Errorf("pass %d: %w", i, err)
			}
			if result.LeafSummariesCreated == leafBefore && result.CondensedSummariesCreated == condensedBefore {
				break // no progress
			}
		}
	}

	tokensAfter, err := c.q.GetContextTokenCount(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("get token count after: %w", err)
	}
	result.TokensAfter = int(tokensAfter)
	result.Duration = time.Since(start)

	return result, nil
}

// runPasses fetches context items once and runs both leaf and condensed passes.
func (c *compactionEngine) runPasses(ctx context.Context, convID string, result *memory.CompactionResult) error {
	items, err := c.q.GetContextItems(ctx, convID)
	if err != nil {
		return fmt.Errorf("get context items: %w", err)
	}
	if err := c.leafPass(ctx, convID, items, result); err != nil {
		return fmt.Errorf("leaf pass: %w", err)
	}
	// Re-fetch after leaf pass may have mutated context items.
	items, err = c.q.GetContextItems(ctx, convID)
	if err != nil {
		return fmt.Errorf("get context items: %w", err)
	}
	if err := c.condensedPass(ctx, convID, items, result); err != nil {
		return fmt.Errorf("condensed pass: %w", err)
	}
	return nil
}

// leafPass finds eligible message chunks outside the fresh tail and creates leaf summaries.
func (c *compactionEngine) leafPass(ctx context.Context, convID string, items []sqlc.CtxItem, result *memory.CompactionResult) error {
	_, older := splitFreshTail(items, c.freshTail)

	runs := findMessageRuns(older, defaultLeafChunkSize)
	if len(runs) == 0 {
		return nil
	}

	prepared := make([]messageRunSummary, len(runs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(summarizeConcurrency)
	for i, run := range runs {
		group.Go(func() error {
			summary, err := c.summarizeMessageRun(groupCtx, convID, run)
			if err != nil {
				return err
			}
			prepared[i] = summary
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	// Summaries in a pass are written only after every LLM call succeeds. This
	// intentionally makes each pass all-or-nothing; older sequential code could
	// leave earlier runs committed when a later summarization failed.
	for _, summary := range prepared {
		if err := c.writeMessageRunSummary(ctx, convID, summary, result); err != nil {
			return err
		}
	}

	return nil
}

// messageRun represents a contiguous sequence of message context items.
type messageRun struct {
	items    []sqlc.CtxItem
	startOrd int64
	endOrd   int64
}

// findMessageRuns finds contiguous sequences of message items with at least minSize messages.
// Runs are trimmed so they never start with an orphan tool_result or end with an orphan
// tool_call — this prevents compaction from splitting tool_call/tool_result pairs.
func findMessageRuns(items []sqlc.CtxItem, minSize int) []messageRun {
	var runs []messageRun
	var current []sqlc.CtxItem

	flush := func() {
		current = trimOrphanedToolPairs(current)
		if len(current) >= minSize {
			runs = append(runs, messageRun{
				items:    current,
				startOrd: current[0].Ordinal,
				endOrd:   current[len(current)-1].Ordinal,
			})
		}
		current = nil
	}

	for _, item := range items {
		if item.ItemType == itemTypeMessage {
			current = append(current, item)
		} else {
			flush()
		}
	}
	flush()

	return runs
}

// trimOrphanedToolPairs removes leading tool_results (whose tool_call is outside
// this run) and trailing tool_calls (whose tool_result is outside this run).
// The trimmed items stay as raw context items and will be handled in a future
// compaction pass when they naturally pair with their counterparts.
func trimOrphanedToolPairs(items []sqlc.CtxItem) []sqlc.CtxItem {
	if len(items) == 0 {
		return items
	}
	start := 0
	for start < len(items) && items[start].EventType == eventTypeToolResult {
		start++
	}
	end := len(items)
	for end > start && items[end-1].EventType == eventTypeToolCall {
		end--
	}
	return items[start:end]
}

// formatMessageForSummarizer formats a CtxMessage for compaction summarization.
// Tool results and tool calls are rendered compactly; all other messages keep
// the original [role] content shape. On any JSON unmarshal error the original
// format is returned as a safe fallback on JSON parse errors.
func formatMessageForSummarizer(msg sqlc.CtxMessage) string {
	return formatMessageForSummarizerWithParts(msg, nil)
}

func formatMessageForSummarizerWithParts(msg sqlc.CtxMessage, parts []loadedMessagePart) string {
	if len(parts) > 0 {
		text := stablePartText(parts)
		if msg.EventType == eventTypeToolResult {
			var env toolResultEnvelope
			if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
				return formatSummarizerInput(msg, text)
			}
			if env.Error != "" {
				return fmt.Sprintf("[tool:%s] error: %s", env.Tool, text)
			}
			return fmt.Sprintf("[tool:%s] result(%d chars): %s", env.Tool, len(text), truncateUTF8(text, 300))
		}
		return formatSummarizerInput(msg, text)
	}
	switch msg.EventType {
	case eventTypeMultimodal:
		// A multimodal user message stores its blocks as JSON with the image's
		// base64 inline. The default branch below would splat that whole payload
		// into the summarizer prompt — megabytes of base64 for one screenshot —
		// so keep the text and name the images instead of carrying their bytes.
		var blocks []contentBlockJSON
		if err := json.Unmarshal([]byte(msg.Content), &blocks); err != nil {
			return formatSummarizerInput(msg, truncateUTF8(msg.Content, 300))
		}
		var parts []string
		images := 0
		for _, b := range blocks {
			switch b.Kind {
			case "text":
				if b.Text != "" {
					parts = append(parts, b.Text)
				}
			case "image":
				images++
				parts = append(parts, fmt.Sprintf("[image %d omitted (%s)]", images, b.MimeType))
			}
		}
		return formatSummarizerInput(msg, strings.Join(parts, "\n"))

	case eventTypeToolResult:
		var env toolResultEnvelope
		if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
			return formatSummarizerInput(msg, msg.Content)
		}
		if env.Error != "" {
			return fmt.Sprintf("[tool:%s] error: %s", env.Tool, env.Error)
		}
		var text string
		if err := json.Unmarshal(env.Result, &text); err != nil {
			return formatSummarizerInput(msg, msg.Content)
		}
		preview := truncateUTF8(text, 300)
		return fmt.Sprintf("[tool:%s] result(%d chars): %s", env.Tool, len(text), preview)

	case eventTypeToolCall:
		var env toolCallEnvelope
		if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
			return formatSummarizerInput(msg, msg.Content)
		}
		args := truncateUTF8(string(env.Args), 300)
		return fmt.Sprintf("[assistant:call %s] args: %s", env.Tool, args)

	default:
		return formatSummarizerInput(msg, msg.Content)
	}
}

func formatSummarizerInput(msg sqlc.CtxMessage, content string) string {
	if msg.Role != roleUser {
		return fmt.Sprintf("[%s] %s", msg.Role, content)
	}
	switch eventlog.ActorType(msg.ActorType) {
	case eventlog.ActorAgent:
		source := msg.SourceSessionID.String
		if source == "" {
			source = msg.ActorID.String
		}
		if source == "" {
			source = "unknown"
		}
		return fmt.Sprintf("[agent-input from %s] %s", source, content)
	case eventlog.ActorSystem:
		return fmt.Sprintf("[system-input] %s", content)
	default:
		return fmt.Sprintf("[user] %s", content)
	}
}

type messageRunSummary struct {
	run                       messageRun
	messages                  []sqlc.CtxMessage
	content                   string
	totalTokens               int64
	earliestAt                time.Time
	latestAt                  time.Time
	containsNonPrincipalInput bool
}

// summarizeMessageRun creates a leaf summary candidate from a message run without writing it.
func (c *compactionEngine) summarizeMessageRun(ctx context.Context, convID string, run messageRun) (messageRunSummary, error) {
	// Load source messages before opening a transaction. The summarizer may call
	// back into the database to resolve model/provider settings; holding a
	// transaction across the LLM call would pin a pooled connection (and risk the
	// idle-in-transaction timeout) for the whole summarization, so do the slow work
	// first and keep the write tx short.
	var messages []sqlc.CtxMessage
	var textParts []string
	var totalTokens int64
	var earliestAt, latestAt time.Time
	var containsNonPrincipalInput bool

	messageIDs := make([]string, 0, len(run.items))
	for _, item := range run.items {
		if item.MessageID.Valid {
			messageIDs = append(messageIDs, item.MessageID.String)
		}
	}
	loadedMessages, err := c.q.ListMessagesByIDs(ctx, sqlc.ListMessagesByIDsParams{ConversationID: convID, MessageIds: messageIDs})
	if err != nil {
		return messageRunSummary{}, fmt.Errorf("list messages: %w", err)
	}
	messagesByID := make(map[string]sqlc.CtxMessage, len(loadedMessages))
	for _, msg := range loadedMessages {
		messagesByID[msg.ID] = msg
	}
	partsByMessage, err := loadMessageParts(ctx, c.q, messageIDsThatCanHaveParts(loadedMessages))
	if err != nil {
		return messageRunSummary{}, err
	}
	for _, item := range run.items {
		if !item.MessageID.Valid {
			continue
		}
		msg, ok := messagesByID[item.MessageID.String]
		if !ok {
			return messageRunSummary{}, fmt.Errorf("get message %s: %w", item.MessageID.String, pgx.ErrNoRows)
		}
		messages = append(messages, msg)
		textParts = append(textParts, formatMessageForSummarizerWithParts(msg, partsByMessage[msg.ID]))
		totalTokens += msg.TokenCount
		containsNonPrincipalInput = containsNonPrincipalInput ||
			(msg.Role == roleUser && eventlog.ActorType(msg.ActorType) == eventlog.ActorAgent)

		if earliestAt.IsZero() || msg.CreatedAt.Before(earliestAt) {
			earliestAt = msg.CreatedAt
		}
		if msg.CreatedAt.After(latestAt) {
			latestAt = msg.CreatedAt
		}
	}

	if len(messages) == 0 {
		return messageRunSummary{run: run}, nil
	}

	// Generate summary outside the write transaction. The provider-level
	// per-session lock still prevents same-session Append/Compact races.
	text := strings.Join(textParts, "\n")
	targetTokens := max(int(totalTokens)/3, 10)

	summary, err := c.summarizer.Summarize(ctx, text, memory.SummarizeOptions{
		TargetTokens: targetTokens,
	})
	if err != nil {
		return messageRunSummary{}, fmt.Errorf("summarize: %w", err)
	}

	return messageRunSummary{
		run:                       run,
		messages:                  messages,
		content:                   summary,
		totalTokens:               totalTokens,
		earliestAt:                earliestAt,
		latestAt:                  latestAt,
		containsNonPrincipalInput: containsNonPrincipalInput,
	}, nil
}

func (c *compactionEngine) writeMessageRunSummary(ctx context.Context, convID string, summary messageRunSummary, result *memory.CompactionResult) error {
	if len(summary.messages) == 0 {
		return nil
	}

	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)

	// Serialize this writeback against concurrent Appends/compactions on the same
	// conversation (cross-node). The LLM summarization ran BEFORE this short tx, so
	// the lock is never held across the model call; it only guards the
	// delete-range + summary-item rewrite below. Released with the tx.
	if err = qtx.LockConversationForWrite(ctx, convID); err != nil {
		return fmt.Errorf("lock conversation: %w", err)
	}
	if err = agentrun.ValidateTx(ctx, tx); err != nil {
		return err
	}

	// Create summary record.
	sumID := generateSummaryID()
	err = qtx.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID:                        sumID,
		ConversationID:            convID,
		Kind:                      kindLeaf,
		Depth:                     0,
		Content:                   summary.content,
		TokenCount:                int64(memory.EstimateTokens(summary.content)),
		EarliestAt:                pgtype.Timestamptz{Time: summary.earliestAt.UTC(), Valid: !summary.earliestAt.IsZero()},
		LatestAt:                  pgtype.Timestamptz{Time: summary.latestAt.UTC(), Valid: !summary.latestAt.IsZero()},
		DescendantCount:           int64(len(summary.messages)),
		DescendantTokenCount:      summary.totalTokens,
		SourceMessageTokenCount:   summary.totalTokens,
		ContainsNonPrincipalInput: summary.containsNonPrincipalInput,
	})
	if err != nil {
		return fmt.Errorf("create summary: %w", err)
	}

	// Link summary to source messages.
	for i, msg := range summary.messages {
		err = qtx.LinkSummaryToMessage(ctx, sqlc.LinkSummaryToMessageParams{
			SummaryID: sumID,
			MessageID: msg.ID,
			Ordinal:   int64(i),
		})
		if err != nil {
			return fmt.Errorf("link message %s: %w", msg.ID, err)
		}
	}

	// Replace message context items with summary item.
	err = qtx.DeleteContextItemsInRange(ctx, sqlc.DeleteContextItemsInRangeParams{
		ConversationID: convID,
		Ordinal:        summary.run.startOrd,
		Ordinal_2:      summary.run.endOrd,
	})
	if err != nil {
		return fmt.Errorf("delete context range: %w", err)
	}

	err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        summary.run.startOrd, // reuse start ordinal
		ItemType:       itemTypeSummary,
		SummaryID:      pgtype.Text{String: sumID, Valid: true},
		Role:           "",
	})
	if err != nil {
		return fmt.Errorf("insert summary context item: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	result.LeafSummariesCreated++
	result.MessagesCompacted += len(summary.messages)

	return nil
}

// condensedPass finds eligible same-depth summaries and creates condensed summaries.
func (c *compactionEngine) condensedPass(ctx context.Context, convID string, items []sqlc.CtxItem, result *memory.CompactionResult) error {
	// Pre-fetch all summary items so we can group by depth and avoid re-fetching.
	sumCache, err := c.loadSummaryCache(ctx, convID, items)
	if err != nil {
		return err
	}
	depthOf := make(map[string]int64, len(sumCache))
	for id, sum := range sumCache {
		depthOf[id] = sum.Depth
	}

	// Find runs of consecutive summary items at the same depth.
	runs := findSummaryRuns(items, 2, depthOf) // need at least 2 summaries to condense
	if len(runs) == 0 {
		return nil
	}

	prepared := make([]condensedRunSummary, len(runs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(summarizeConcurrency)
	for i, run := range runs {
		group.Go(func() error {
			summary, err := c.summarizeCondensedRun(groupCtx, run, sumCache)
			if err != nil {
				return err
			}
			prepared[i] = summary
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	// See leafPass: writes stay serial and happen only after all summaries succeed.
	for _, summary := range prepared {
		if err := c.writeCondensedRunSummary(ctx, convID, summary, result); err != nil {
			return err
		}
	}

	return nil
}

func (c *compactionEngine) loadSummaryCache(ctx context.Context, convID string, items []sqlc.CtxItem) (map[string]sqlc.CtxSummary, error) {
	summaryIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.ItemType != itemTypeSummary || !item.SummaryID.Valid {
			continue
		}
		id := item.SummaryID.String
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		summaryIDs = append(summaryIDs, id)
	}
	if len(summaryIDs) == 0 {
		return map[string]sqlc.CtxSummary{}, nil
	}
	summaries, err := c.q.ListSummariesByIDs(ctx, sqlc.ListSummariesByIDsParams{ConversationID: convID, SummaryIds: summaryIDs})
	if err != nil {
		return nil, fmt.Errorf("list summaries: %w", err)
	}
	out := make(map[string]sqlc.CtxSummary, len(summaries))
	for _, sum := range summaries {
		out[sum.ID] = sum
	}
	for _, id := range summaryIDs {
		if _, ok := out[id]; !ok {
			return nil, fmt.Errorf("get summary depth %s: %w", id, pgx.ErrNoRows)
		}
	}
	return out, nil
}

// summaryRun represents a contiguous sequence of summary context items.
type summaryRun struct {
	items    []sqlc.CtxItem
	startOrd int64
	endOrd   int64
}

// findSummaryRuns finds contiguous sequences of summary items at the same depth
// with at least minSize items. Runs are broken when depth changes.
func findSummaryRuns(items []sqlc.CtxItem, minSize int, depthOf map[string]int64) []summaryRun {
	flushRun := func(current []sqlc.CtxItem, runs []summaryRun) []summaryRun {
		if len(current) >= minSize {
			runs = append(runs, summaryRun{
				items:    current,
				startOrd: current[0].Ordinal,
				endOrd:   current[len(current)-1].Ordinal,
			})
		}
		return runs
	}

	var runs []summaryRun
	var current []sqlc.CtxItem
	var currentDepth int64

	for _, item := range items {
		if item.ItemType != itemTypeSummary {
			runs = flushRun(current, runs)
			current = nil
			continue
		}

		depth := depthOf[item.SummaryID.String]
		if len(current) > 0 && depth != currentDepth {
			runs = flushRun(current, runs)
			current = nil
		}

		current = append(current, item)
		currentDepth = depth
	}
	runs = flushRun(current, runs)

	return runs
}

type condensedRunSummary struct {
	run                       summaryRun
	summaries                 []sqlc.CtxSummary
	content                   string
	newDepth                  int64
	totalTokens               int64
	totalDescendants          int64
	totalDescTokens           int64
	earliestAt                time.Time
	latestAt                  time.Time
	containsNonPrincipalInput bool
}

// summarizeCondensedRun creates a condensed summary candidate from summary items without writing it.
func (c *compactionEngine) summarizeCondensedRun(ctx context.Context, run summaryRun, sumCache map[string]sqlc.CtxSummary) (condensedRunSummary, error) {
	// Load summaries from cache before opening a transaction; see
	// summarizeMessageRun for why LLM work must not happen while holding a pooled
	// connection in a transaction.
	var summaries []sqlc.CtxSummary
	var textParts []string
	var totalTokens int64
	var totalDescendants int64
	var totalDescTokens int64
	var maxDepth int64
	var earliestAt, latestAt time.Time
	var containsNonPrincipalInput bool

	for _, item := range run.items {
		if !item.SummaryID.Valid {
			continue
		}
		sum, ok := sumCache[item.SummaryID.String]
		if !ok {
			return condensedRunSummary{}, fmt.Errorf("summary %s not in cache", item.SummaryID.String)
		}
		summaries = append(summaries, sum)
		textParts = append(textParts, sum.Content)
		totalTokens += sum.TokenCount
		totalDescendants += sum.DescendantCount
		totalDescTokens += sum.DescendantTokenCount
		containsNonPrincipalInput = containsNonPrincipalInput || sum.ContainsNonPrincipalInput
		if sum.Depth > maxDepth {
			maxDepth = sum.Depth
		}
		if sum.EarliestAt.Valid && (earliestAt.IsZero() || sum.EarliestAt.Time.Before(earliestAt)) {
			earliestAt = sum.EarliestAt.Time.UTC()
		}
		if sum.LatestAt.Valid && sum.LatestAt.Time.After(latestAt) {
			latestAt = sum.LatestAt.Time.UTC()
		}
	}

	if len(summaries) < 2 {
		return condensedRunSummary{run: run}, nil
	}

	// Generate condensed summary.
	text := strings.Join(textParts, "\n\n---\n\n")
	newDepth := maxDepth + 1
	// Condensed summaries are already compressed, so use /2 (not /3 like leaf)
	// for less aggressive reduction to preserve detail.
	targetTokens := max(int(totalTokens)/2, 10)

	content, err := c.summarizer.Summarize(ctx, text, memory.SummarizeOptions{
		IsCondensed:  true,
		Depth:        int(newDepth),
		TargetTokens: targetTokens,
	})
	if err != nil {
		return condensedRunSummary{}, fmt.Errorf("summarize condensed: %w", err)
	}

	return condensedRunSummary{
		run:                       run,
		summaries:                 summaries,
		content:                   content,
		newDepth:                  newDepth,
		totalTokens:               totalTokens,
		totalDescendants:          totalDescendants,
		totalDescTokens:           totalDescTokens,
		earliestAt:                earliestAt,
		latestAt:                  latestAt,
		containsNonPrincipalInput: containsNonPrincipalInput,
	}, nil
}

func (c *compactionEngine) writeCondensedRunSummary(ctx context.Context, convID string, summary condensedRunSummary, result *memory.CompactionResult) error {
	if len(summary.summaries) < 2 {
		return nil
	}

	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)

	// Serialize this writeback against concurrent Appends/compactions on the same
	// conversation (cross-node); the LLM ran before this short tx so the lock is
	// never held across the model call. Released with the tx.
	if err = qtx.LockConversationForWrite(ctx, convID); err != nil {
		return fmt.Errorf("lock conversation: %w", err)
	}
	if err = agentrun.ValidateTx(ctx, tx); err != nil {
		return err
	}

	// Create condensed summary.
	sumID := generateSummaryID()
	err = qtx.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID:                        sumID,
		ConversationID:            convID,
		Kind:                      kindCondensed,
		Depth:                     summary.newDepth,
		Content:                   summary.content,
		TokenCount:                int64(memory.EstimateTokens(summary.content)),
		EarliestAt:                pgtype.Timestamptz{Time: summary.earliestAt.UTC(), Valid: !summary.earliestAt.IsZero()},
		LatestAt:                  pgtype.Timestamptz{Time: summary.latestAt.UTC(), Valid: !summary.latestAt.IsZero()},
		DescendantCount:           summary.totalDescendants + int64(len(summary.summaries)),
		DescendantTokenCount:      summary.totalDescTokens + summary.totalTokens,
		SourceMessageTokenCount:   0,
		ContainsNonPrincipalInput: summary.containsNonPrincipalInput,
	})
	if err != nil {
		return fmt.Errorf("create condensed summary: %w", err)
	}

	// Link to parent summaries.
	for i, sum := range summary.summaries {
		err = qtx.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
			SummaryID:       sumID,
			ParentSummaryID: sum.ID,
			Ordinal:         int64(i),
		})
		if err != nil {
			return fmt.Errorf("link parent %s: %w", sum.ID, err)
		}
	}

	// Replace summary context items with condensed item.
	err = qtx.DeleteContextItemsInRange(ctx, sqlc.DeleteContextItemsInRangeParams{
		ConversationID: convID,
		Ordinal:        summary.run.startOrd,
		Ordinal_2:      summary.run.endOrd,
	})
	if err != nil {
		return fmt.Errorf("delete context range: %w", err)
	}

	err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        summary.run.startOrd,
		ItemType:       itemTypeSummary,
		SummaryID:      pgtype.Text{String: sumID, Valid: true},
		Role:           "",
	})
	if err != nil {
		return fmt.Errorf("insert condensed context item: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	result.CondensedSummariesCreated++

	return nil
}

// generateSummaryID creates a "sum_" prefixed random hex ID.
func generateSummaryID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sum_" + hex.EncodeToString(b)
}
