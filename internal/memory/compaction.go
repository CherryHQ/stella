package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
)

// CompactionEngine runs leaf and condensed compaction passes.
type CompactionEngine struct {
	db         *sql.DB
	q          *sqlc.Queries
	summarizer Summarizer
	freshTail  int
}

// NewCompactionEngine creates a new compaction engine.
func NewCompactionEngine(db *sql.DB, q *sqlc.Queries, summarizer Summarizer, freshTail int) *CompactionEngine {
	if freshTail <= 0 {
		freshTail = DefaultFreshTail
	}
	return &CompactionEngine{
		db:         db,
		q:          q,
		summarizer: summarizer,
		freshTail:  freshTail,
	}
}

// Compact runs compaction on a conversation based on the given mode.
func (c *CompactionEngine) Compact(ctx context.Context, convID int64, mode CompactionMode) (*CompactionResult, error) {
	start := time.Now()
	result := &CompactionResult{}

	tokensBefore, err := c.q.GetContextTokenCount(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("get token count: %w", err)
	}
	result.TokensBefore = int(tokensBefore)

	switch mode {
	case CompactionIncremental:
		if err := c.runPasses(ctx, convID, result); err != nil {
			return nil, err
		}

	case CompactionFull:
		// Repeat leaf+condensed passes until no more compaction is possible.
		for i := 0; i < 10; i++ { // safety limit
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
func (c *CompactionEngine) runPasses(ctx context.Context, convID int64, result *CompactionResult) error {
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
func (c *CompactionEngine) leafPass(ctx context.Context, convID int64, items []sqlc.CtxItem, result *CompactionResult) error {
	// Find compactable message runs outside the fresh tail.
	_, older := splitFreshTail(items, c.freshTail)

	// Collect contiguous message runs from older items.
	runs := findMessageRuns(older, DefaultLeafChunkSize)
	if len(runs) == 0 {
		return nil
	}

	for _, run := range runs {
		if err := c.compactMessageRun(ctx, convID, run, result); err != nil {
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
func findMessageRuns(items []sqlc.CtxItem, minSize int) []messageRun {
	var runs []messageRun
	var current []sqlc.CtxItem

	for _, item := range items {
		if item.ItemType == ItemTypeMessage {
			current = append(current, item)
		} else {
			if len(current) >= minSize {
				runs = append(runs, messageRun{
					items:    current,
					startOrd: current[0].Ordinal,
					endOrd:   current[len(current)-1].Ordinal,
				})
			}
			current = nil
		}
	}
	// Don't forget the last run.
	if len(current) >= minSize {
		runs = append(runs, messageRun{
			items:    current,
			startOrd: current[0].Ordinal,
			endOrd:   current[len(current)-1].Ordinal,
		})
	}

	return runs
}

// compactMessageRun creates a leaf summary from a message run within a transaction.
func (c *CompactionEngine) compactMessageRun(ctx context.Context, convID int64, run messageRun, result *CompactionResult) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := c.q.WithTx(tx)

	// Load messages for this run.
	var messages []sqlc.CtxMessage
	var textParts []string
	var totalTokens int64
	var earliestAt, latestAt string

	for _, item := range run.items {
		if !item.MessageID.Valid {
			continue
		}
		msg, err := qtx.GetMessage(ctx, item.MessageID.Int64)
		if err != nil {
			return fmt.Errorf("get message %d: %w", item.MessageID.Int64, err)
		}
		messages = append(messages, msg)
		textParts = append(textParts, fmt.Sprintf("[%s] %s", msg.Role, msg.Content))
		totalTokens += msg.TokenCount

		if earliestAt == "" || msg.CreatedAt < earliestAt {
			earliestAt = msg.CreatedAt
		}
		if msg.CreatedAt > latestAt {
			latestAt = msg.CreatedAt
		}
	}

	if len(messages) == 0 {
		return nil
	}

	// Generate summary.
	text := strings.Join(textParts, "\n")
	targetTokens := int(totalTokens) / 3
	if targetTokens < 10 {
		targetTokens = 10
	}

	summary, err := c.summarizer.Summarize(ctx, text, SummarizeOptions{
		TargetTokens: targetTokens,
	})
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}

	// Create summary record.
	sumID := generateSummaryID()
	err = qtx.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID:                      sumID,
		ConversationID:          convID,
		Kind:                    KindLeaf,
		Depth:                   0,
		Content:                 summary,
		TokenCount:              int64(EstimateTokens(summary)),
		EarliestAt:              sql.NullString{String: earliestAt, Valid: earliestAt != ""},
		LatestAt:                sql.NullString{String: latestAt, Valid: latestAt != ""},
		DescendantCount:         int64(len(messages)),
		DescendantTokenCount:    totalTokens,
		SourceMessageTokenCount: totalTokens,
	})
	if err != nil {
		return fmt.Errorf("create summary: %w", err)
	}

	// Link summary to source messages.
	for i, msg := range messages {
		err = qtx.LinkSummaryToMessage(ctx, sqlc.LinkSummaryToMessageParams{
			SummaryID: sumID,
			MessageID: msg.ID,
			Ordinal:   int64(i),
		})
		if err != nil {
			return fmt.Errorf("link message %d: %w", msg.ID, err)
		}
	}

	// Replace message context items with summary item.
	err = qtx.DeleteContextItemsInRange(ctx, sqlc.DeleteContextItemsInRangeParams{
		ConversationID: convID,
		Ordinal:        run.startOrd,
		Ordinal_2:      run.endOrd,
	})
	if err != nil {
		return fmt.Errorf("delete context range: %w", err)
	}

	err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        run.startOrd, // reuse start ordinal
		ItemType:       ItemTypeSummary,
		SummaryID:      sql.NullString{String: sumID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("insert summary context item: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	result.LeafSummariesCreated++
	result.MessagesCompacted += len(messages)

	return nil
}

// condensedPass finds eligible same-depth summaries and creates condensed summaries.
func (c *CompactionEngine) condensedPass(ctx context.Context, convID int64, items []sqlc.CtxItem, result *CompactionResult) error {
	// Pre-fetch all summary items so we can group by depth and avoid re-fetching.
	sumCache := make(map[string]sqlc.CtxSummary)
	depthOf := make(map[string]int64)
	for _, item := range items {
		if item.ItemType != ItemTypeSummary || !item.SummaryID.Valid {
			continue
		}
		sum, err := c.q.GetSummary(ctx, item.SummaryID.String)
		if err != nil {
			return fmt.Errorf("get summary depth %s: %w", item.SummaryID.String, err)
		}
		sumCache[item.SummaryID.String] = sum
		depthOf[item.SummaryID.String] = sum.Depth
	}

	// Find runs of consecutive summary items at the same depth.
	runs := findSummaryRuns(items, 2, depthOf) // need at least 2 summaries to condense
	if len(runs) == 0 {
		return nil
	}

	for _, run := range runs {
		if err := c.condenseSummaryRun(ctx, convID, run, sumCache, result); err != nil {
			return err
		}
	}

	return nil
}

// summaryRun represents a contiguous sequence of summary context items.
type summaryRun struct {
	items    []sqlc.CtxItem
	startOrd int64
	endOrd   int64
}

// findSummaryRuns finds contiguous sequences of summary items at the same depth
// with at least minSize items. The depthOf map provides the depth for each summary ID.
// Runs are broken when depth changes to preserve the DAG hierarchy.
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
		if item.ItemType != ItemTypeSummary {
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

// condenseSummaryRun creates a condensed summary from a run of summary context items.
func (c *CompactionEngine) condenseSummaryRun(ctx context.Context, convID int64, run summaryRun, sumCache map[string]sqlc.CtxSummary, result *CompactionResult) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := c.q.WithTx(tx)

	// Load summaries from cache.
	var summaries []sqlc.CtxSummary
	var textParts []string
	var totalTokens int64
	var totalDescendants int64
	var totalDescTokens int64
	var maxDepth int64
	var earliestAt, latestAt string

	for _, item := range run.items {
		if !item.SummaryID.Valid {
			continue
		}
		sum, ok := sumCache[item.SummaryID.String]
		if !ok {
			return fmt.Errorf("summary %s not in cache", item.SummaryID.String)
		}
		summaries = append(summaries, sum)
		textParts = append(textParts, sum.Content)
		totalTokens += sum.TokenCount
		totalDescendants += sum.DescendantCount
		totalDescTokens += sum.DescendantTokenCount
		if sum.Depth > maxDepth {
			maxDepth = sum.Depth
		}
		if sum.EarliestAt.Valid && (earliestAt == "" || sum.EarliestAt.String < earliestAt) {
			earliestAt = sum.EarliestAt.String
		}
		if sum.LatestAt.Valid && sum.LatestAt.String > latestAt {
			latestAt = sum.LatestAt.String
		}
	}

	if len(summaries) < 2 {
		return nil
	}

	// Generate condensed summary.
	text := strings.Join(textParts, "\n\n---\n\n")
	newDepth := maxDepth + 1
	// Condensed summaries are already compressed, so use /2 (not /3 like leaf)
	// for less aggressive reduction to preserve detail.
	targetTokens := int(totalTokens) / 2
	if targetTokens < 10 {
		targetTokens = 10
	}

	content, err := c.summarizer.Summarize(ctx, text, SummarizeOptions{
		IsCondensed:  true,
		Depth:        int(newDepth),
		TargetTokens: targetTokens,
	})
	if err != nil {
		return fmt.Errorf("summarize condensed: %w", err)
	}

	// Create condensed summary.
	sumID := generateSummaryID()
	err = qtx.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID:                      sumID,
		ConversationID:          convID,
		Kind:                    KindCondensed,
		Depth:                   newDepth,
		Content:                 content,
		TokenCount:              int64(EstimateTokens(content)),
		EarliestAt:              sql.NullString{String: earliestAt, Valid: earliestAt != ""},
		LatestAt:                sql.NullString{String: latestAt, Valid: latestAt != ""},
		DescendantCount:         totalDescendants + int64(len(summaries)),
		DescendantTokenCount:    totalDescTokens + totalTokens,
		SourceMessageTokenCount: 0,
	})
	if err != nil {
		return fmt.Errorf("create condensed summary: %w", err)
	}

	// Link to parent summaries.
	for i, sum := range summaries {
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
		Ordinal:        run.startOrd,
		Ordinal_2:      run.endOrd,
	})
	if err != nil {
		return fmt.Errorf("delete context range: %w", err)
	}

	err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        run.startOrd,
		ItemType:       ItemTypeSummary,
		SummaryID:      sql.NullString{String: sumID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("insert condensed context item: %w", err)
	}

	if err := tx.Commit(); err != nil {
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
