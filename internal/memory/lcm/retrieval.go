package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Default retrieval limits.
const (
	defaultSearchLimit  = 20
	defaultExpandTokens = 4000
	maxContentSnippet   = 500
)

// retrievalEngine provides search and exploration of compacted history.
type retrievalEngine struct {
	q *sqlc.Queries
}

func newRetrievalEngine(q *sqlc.Queries) *retrievalEngine {
	return &retrievalEngine{q: q}
}

// Search implements memory.Searcher.
func (p *Provider) Search(ctx context.Context, session memory.Session, query memory.SearchQuery) ([]memory.SearchResult, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: session.ID, UserID: sql.NullString{String: session.UserID, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	return p.retrieval.search(ctx, conv.ID, query)
}

func (r *retrievalEngine) search(ctx context.Context, convID string, query memory.SearchQuery) ([]memory.SearchResult, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	likePattern := "%" + query.Text + "%"

	scope := scopeBoth
	switch query.Scope {
	case memory.SearchScopeMessages:
		scope = scopeMessages
	case memory.SearchScopeSummaries:
		scope = scopeSummaries
	}

	var results []memory.SearchResult

	if scope == scopeMessages || scope == scopeBoth {
		msgs, err := r.q.SearchMessages(ctx, sqlc.SearchMessagesParams{
			ConversationID: convID,
			Content:        likePattern,
			Limit:          int64(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		for _, msg := range msgs {
			results = append(results, memory.SearchResult{
				SourceType: itemTypeMessage,
				SourceID:   fmt.Sprint(msg.ID),
				Content:    truncateUTF8(msg.Content, maxContentSnippet),
				Timestamp:  parseTime(msg.CreatedAt),
			})
		}
	}

	if scope == scopeSummaries || scope == scopeBoth {
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
			results = append(results, memory.SearchResult{
				SourceType: itemTypeSummary,
				SourceID:   s.ID,
				Content:    truncateUTF8(s.Content, maxContentSnippet),
				Timestamp:  parseTime(s.CreatedAt),
			})
		}
	}

	return results, nil
}

// Describe implements memory.Explorer.
func (p *Provider) Describe(ctx context.Context, summaryID string) (*memory.DescribeResult, error) {
	return p.retrieval.describe(ctx, summaryID)
}

func (r *retrievalEngine) describe(ctx context.Context, summaryID string) (*memory.DescribeResult, error) {
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

	return &memory.DescribeResult{
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

// Expand implements memory.Explorer.
func (p *Provider) Expand(ctx context.Context, summaryID string, tokenCap int) (*memory.ExpandResult, error) {
	return p.retrieval.expand(ctx, summaryID, tokenCap)
}

func (r *retrievalEngine) expand(ctx context.Context, summaryID string, tokenCap int) (*memory.ExpandResult, error) {
	if tokenCap <= 0 {
		tokenCap = defaultExpandTokens
	}

	sum, err := r.q.GetSummary(ctx, summaryID)
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}

	result := &memory.ExpandResult{SummaryID: summaryID}
	tokensUsed := 0

	if sum.Kind == kindLeaf {
		msgs, err := r.q.GetSummaryMessages(ctx, summaryID)
		if err != nil {
			return nil, fmt.Errorf("get summary messages: %w", err)
		}
		for _, msg := range msgs {
			tokens := memory.EstimateTokens(msg.Content)
			if tokensUsed+tokens > tokenCap && len(result.Messages) > 0 {
				break
			}
			result.Messages = append(result.Messages, memory.ExpandMessage{
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
			tokens := memory.EstimateTokens(child.Content)
			if tokensUsed+tokens > tokenCap && len(result.Children) > 0 {
				break
			}
			result.Children = append(result.Children, memory.ExpandChild{
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
