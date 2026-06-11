package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/db/ftsquery"
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
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return nil, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
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
	match := ftsquery.BuildMatchQuery(query.Text)
	if match == "" {
		return nil, nil
	}

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
			Match:          match,
			Limit:          int64(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		for _, msg := range msgs {
			results = append(results, memory.SearchResult{
				SourceType: itemTypeMessage,
				SourceID:   fmt.Sprint(msg.ID),
				Content:    searchSnippet(msg.Snippet, msg.Content),
				Score:      -msg.Score,
				Timestamp:  parseTime(msg.CreatedAt),
			})
		}
	}

	if scope == scopeSummaries || scope == scopeBoth {
		sums, err := r.q.SearchSummaries(ctx, sqlc.SearchSummariesParams{
			ConversationID: convID,
			Match:          match,
			Limit:          int64(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search summaries: %w", err)
		}
		for _, s := range sums {
			results = append(results, memory.SearchResult{
				SourceType: itemTypeSummary,
				SourceID:   s.ID,
				Content:    searchSnippet(s.Snippet, s.Content),
				Score:      -s.Score,
				Timestamp:  parseTime(s.CreatedAt),
			})
		}
	}

	// Both-scope queries each table with the full limit, then keep the global
	// top N so a strong summary hit can outrank a weak message hit. BM25 from
	// the two indexes shares the same corpus statistics family closely enough
	// for cross-source ordering.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Timestamp.After(results[j].Timestamp)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// searchSnippet prefers the FTS5 snippet (match context with <<>> highlights)
// and falls back to a plain truncation for degenerate empty snippets.
func searchSnippet(snippet, content string) string {
	if snippet != "" {
		return snippet
	}
	return truncateUTF8(content, maxContentSnippet)
}

func (p *Provider) getScopedSummary(ctx context.Context, summaryID string) (sqlc.CtxSummary, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return sqlc.CtxSummary{}, err
	}
	sum, err := p.q.GetSummaryByID(ctx, summaryID)
	if err != nil {
		return sqlc.CtxSummary{}, fmt.Errorf("get summary: %w", err)
	}
	if _, err := p.q.GetConversation(ctx, sqlc.GetConversationParams{
		ID:      sum.ConversationID,
		UserID:  sql.NullString{String: userID, Valid: true},
		AgentID: nullAgent(agentID),
	}); err != nil {
		return sqlc.CtxSummary{}, fmt.Errorf("get summary conversation: %w", err)
	}
	return sum, nil
}

// Describe implements memory.Explorer.
func (p *Provider) Describe(ctx context.Context, summaryID string) (*memory.DescribeResult, error) {
	sum, err := p.getScopedSummary(ctx, summaryID)
	if err != nil {
		return nil, err
	}
	return p.retrieval.describe(ctx, sum)
}

func (r *retrievalEngine) describe(ctx context.Context, sum sqlc.CtxSummary) (*memory.DescribeResult, error) {
	parents, err := r.q.GetSummaryParents(ctx, sum.ID)
	if err != nil {
		return nil, fmt.Errorf("get parents: %w", err)
	}
	parentIDs := make([]string, len(parents))
	for i, p := range parents {
		parentIDs[i] = p.ID
	}

	children, err := r.q.GetSummaryChildren(ctx, sum.ID)
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
	sum, err := p.getScopedSummary(ctx, summaryID)
	if err != nil {
		return nil, err
	}
	return p.retrieval.expand(ctx, sum, tokenCap)
}

func (r *retrievalEngine) expand(ctx context.Context, sum sqlc.CtxSummary, tokenCap int) (*memory.ExpandResult, error) {
	if tokenCap <= 0 {
		tokenCap = defaultExpandTokens
	}

	result := &memory.ExpandResult{SummaryID: sum.ID}
	tokensUsed := 0

	if sum.Kind == kindLeaf {
		msgs, err := r.q.GetSummaryMessages(ctx, sum.ID)
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
		children, err := r.q.GetSummaryChildren(ctx, sum.ID)
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
