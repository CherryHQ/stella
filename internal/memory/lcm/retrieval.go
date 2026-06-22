package lcm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
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
	return p.retrieval.search(ctx, session.UserID, session.AgentID, query)
}

func (r *retrievalEngine) search(ctx context.Context, userID, agentID string, query memory.SearchQuery) ([]memory.SearchResult, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	scope := scopeBoth
	switch query.Scope {
	case memory.SearchScopeMessages:
		scope = scopeMessages
	case memory.SearchScopeSummaries:
		scope = scopeSummaries
	}

	// Raw user text goes straight to pg_search: paradedb.match tokenizes it with
	// ICU (so short and CJK queries match) and never errors on punctuation, so
	// there is no separate sanitize/fallback tier.
	match := strings.TrimSpace(query.Text)
	if match == "" {
		return nil, nil
	}

	var results []memory.SearchResult

	if scope == scopeMessages || scope == scopeBoth {
		msgs, err := r.q.SearchMessages(ctx, sqlc.SearchMessagesParams{
			UserID:  pgtype.Text{String: userID, Valid: true},
			AgentID: pgnull.Text(agentID),
			Match:   match,
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		for _, msg := range msgs {
			results = append(results, memory.SearchResult{
				SourceType:        itemTypeMessage,
				SourceID:          fmt.Sprint(msg.ID),
				Content:           searchSnippet(msg.Snippet, msg.Content),
				Score:             msg.Score,
				OccurredAt:        msg.CreatedAt.UTC(),
				SessionID:         msg.SessionID,
				ConversationTitle: msg.ConversationTitle.String,
			})
		}
	}

	if scope == scopeSummaries || scope == scopeBoth {
		sums, err := r.q.SearchSummaries(ctx, sqlc.SearchSummariesParams{
			UserID:  pgtype.Text{String: userID, Valid: true},
			AgentID: pgnull.Text(agentID),
			Match:   match,
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search summaries: %w", err)
		}
		for _, s := range sums {
			results = append(results, memory.SearchResult{
				SourceType:        itemTypeSummary,
				SourceID:          s.ID,
				Content:           searchSnippet(s.Snippet, s.Content),
				Score:             s.Score,
				OccurredAt:        summaryContentTime(s.LatestAt, s.CreatedAt),
				SessionID:         s.SessionID,
				ConversationTitle: s.ConversationTitle.String,
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
		return results[i].OccurredAt.After(results[j].OccurredAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// summaryContentTime returns when a summary's underlying content actually
// occurred: latest_at (the real end of the summarized window), falling back to
// created_at (when the summary was generated) only when latest_at is null.
func summaryContentTime(latestAt pgtype.Timestamptz, createdAt time.Time) time.Time {
	if t := parseNullTime(latestAt); t != nil {
		return *t
	}
	return createdAt.UTC()
}

// searchSnippet prefers the pg_search snippet (match context with <b></b>
// highlights) and falls back to a plain truncation for degenerate empty snippets.
func searchSnippet(snippet, content string) string {
	if snippet != "" {
		return snippet
	}
	return truncateUTF8(content, maxContentSnippet)
}

// GetMessage implements memory.MessageReader.
func (p *Provider) GetMessage(ctx context.Context, messageID string) (*memory.MessageDetail, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return nil, err
	}
	row, err := p.q.GetMessageScoped(ctx, sqlc.GetMessageScopedParams{
		ID:      messageID,
		UserID:  pgtype.Text{String: userID, Valid: true},
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return &memory.MessageDetail{
		MessageID:         row.ID,
		Role:              row.Role,
		Content:           row.Content,
		OccurredAt:        row.CreatedAt.UTC(),
		SessionID:         row.SessionID,
		ConversationTitle: row.ConversationTitle.String,
	}, nil
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
		UserID:  pgtype.Text{String: userID, Valid: true},
		AgentID: pgnull.Text(agentID),
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
				CreatedAt: msg.CreatedAt.UTC(),
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
