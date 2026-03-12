package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/vaayne/anna/db/sqlc"
)

// NewRetrievalEngine creates a RetrievalEngine from a Queries instance.
func NewRetrievalEngine(q *sqlc.Queries) *RetrievalEngine {
	return &RetrievalEngine{q: q}
}

// Default retrieval limits.
const (
	defaultGrepLimit    = 20
	defaultExpandTokens = 4000
	maxContentSnippet   = 500
)

// GrepBySession searches messages and/or summaries for a LIKE pattern,
// resolving the conversation ID from a session ID.
func (r *RetrievalEngine) GrepBySession(ctx context.Context, sessionID string, pattern string, scope string, limit int) ([]GrepResult, error) {
	conv, err := r.q.GetConversationBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	return r.Grep(ctx, conv.ID, pattern, scope, limit)
}

// Grep searches messages and/or summaries for a LIKE pattern.
// scope is "messages", "summaries", or "both" (default).
func (r *RetrievalEngine) Grep(ctx context.Context, convID int64, pattern string, scope string, limit int) ([]GrepResult, error) {
	if limit <= 0 {
		limit = defaultGrepLimit
	}
	if scope == "" {
		scope = "both"
	}
	likePattern := "%" + pattern + "%"

	var results []GrepResult

	if scope == "messages" || scope == "both" {
		msgs, err := r.q.SearchMessages(ctx, sqlc.SearchMessagesParams{
			ConversationID: convID,
			Content:        likePattern,
			Limit:          int64(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		for _, msg := range msgs {
			results = append(results, GrepResult{
				SourceType: "message",
				SourceID:   fmt.Sprint(msg.ID),
				Content:    truncateUTF8(msg.Content, maxContentSnippet),
				Timestamp:  parseTime(msg.CreatedAt),
			})
		}
	}

	if scope == "summaries" || scope == "both" {
		remaining := limit - len(results)
		if remaining <= 0 {
			remaining = limit // summaries-only: use full limit
		}
		sums, err := r.q.SearchSummaries(ctx, sqlc.SearchSummariesParams{
			ConversationID: convID,
			Content:        likePattern,
			Limit:          int64(remaining),
		})
		if err != nil {
			return nil, fmt.Errorf("search summaries: %w", err)
		}
		for _, s := range sums {
			results = append(results, GrepResult{
				SourceType: "summary",
				SourceID:   s.ID,
				Content:    truncateUTF8(s.Content, maxContentSnippet),
				Timestamp:  parseTime(s.CreatedAt),
			})
		}
	}

	return results, nil
}

// Describe returns metadata and lineage for a summary.
func (r *RetrievalEngine) Describe(ctx context.Context, summaryID string) (*DescribeResult, error) {
	sum, err := r.q.GetSummary(ctx, summaryID)
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}

	parents, err := r.q.GetSummaryParents(ctx, summaryID)
	if err != nil {
		return nil, fmt.Errorf("get parents: %w", err)
	}
	parentIDs := make([]string, len(parents))
	for i, p := range parents {
		parentIDs[i] = p.ID
	}

	children, err := r.q.GetSummaryChildren(ctx, summaryID)
	if err != nil {
		return nil, fmt.Errorf("get children: %w", err)
	}
	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID
	}

	return &DescribeResult{
		SummaryID:       sum.ID,
		Kind:            sum.Kind,
		Depth:           int(sum.Depth),
		Content:         sum.Content,
		EarliestAt:      parseNullTime(sum.EarliestAt),
		LatestAt:        parseNullTime(sum.LatestAt),
		DescendantCount: int(sum.DescendantCount),
		ParentIDs:       parentIDs,
		ChildIDs:        childIDs,
	}, nil
}

// Expand drills into a summary, returning either source messages (leaf)
// or child summaries (condensed), up to tokenCap tokens.
func (r *RetrievalEngine) Expand(ctx context.Context, summaryID string, tokenCap int) (*ExpandResult, error) {
	if tokenCap <= 0 {
		tokenCap = defaultExpandTokens
	}

	sum, err := r.q.GetSummary(ctx, summaryID)
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}

	result := &ExpandResult{SummaryID: summaryID}
	tokensUsed := 0

	if sum.Kind == KindLeaf {
		msgs, err := r.q.GetSummaryMessages(ctx, summaryID)
		if err != nil {
			return nil, fmt.Errorf("get summary messages: %w", err)
		}
		for _, msg := range msgs {
			tokens := EstimateTokens(msg.Content)
			if tokensUsed+tokens > tokenCap && len(result.Messages) > 0 {
				break
			}
			result.Messages = append(result.Messages, ExpandMessage{
				MessageID: msg.ID,
				Role:      msg.Role,
				Content:   msg.Content,
				CreatedAt: parseTime(msg.CreatedAt),
			})
			tokensUsed += tokens
		}
	} else {
		children, err := r.q.GetSummaryChildren(ctx, summaryID)
		if err != nil {
			return nil, fmt.Errorf("get children: %w", err)
		}
		for _, child := range children {
			tokens := EstimateTokens(child.Content)
			if tokensUsed+tokens > tokenCap && len(result.Children) > 0 {
				break
			}
			result.Children = append(result.Children, ExpandChild{
				SummaryID: child.ID,
				Kind:      child.Kind,
				Depth:     int(child.Depth),
				Content:   child.Content,
			})
			tokensUsed += tokens
		}
	}

	return result, nil
}

// truncateUTF8 truncates s to at most maxLen runes, appending "..." if truncated.
func truncateUTF8(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "..."
}

// parseTime parses a SQLite datetime string.
func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseNullTime parses a sql.NullString time field into *time.Time.
func parseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	if t.IsZero() {
		return nil
	}
	return &t
}
