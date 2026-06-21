package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/db/ftsquery"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const maxContentSnippet = 500

type LexicalSearch interface {
	SearchMessages(ctx context.Context, query MessageQuery) ([]MessageHit, error)
	SearchSummaries(ctx context.Context, query SummaryQuery) ([]SummaryHit, error)
	SearchArticles(ctx context.Context, query ArticleQuery) ([]ArticleHit, error)
}

type MessageQuery struct {
	UserID  string
	AgentID pgtype.Text
	Text    string
	Limit   int
}

type SummaryQuery struct {
	UserID  string
	AgentID pgtype.Text
	Text    string
	Limit   int
}

type ArticleQuery struct {
	UserID string
	Text   string
	Limit  int
}

type MessageHit struct {
	ID                string
	Content           string
	Score             float64
	CreatedAt         time.Time
	SessionID         string
	ConversationTitle string
}

type SummaryHit struct {
	ID                string
	Content           string
	Score             float64
	CreatedAt         time.Time
	LatestAt          pgtype.Timestamptz
	SessionID         string
	ConversationTitle string
}

type ArticleHit struct {
	Article sqlc.RecallyArticle
	Snippet string
	Score   float64
}

type NativePostgresSearch struct {
	q *sqlc.Queries
}

func NewNativePostgres(q *sqlc.Queries) *NativePostgresSearch {
	return &NativePostgresSearch{q: q}
}

func (s *NativePostgresSearch) SearchMessages(ctx context.Context, query MessageQuery) ([]MessageHit, error) {
	limit := normalizeLimit(query.Limit)
	match := ftsquery.BuildMatchQuery(query.Text)
	if match == "" {
		text := strings.TrimSpace(query.Text)
		if text == "" {
			return nil, nil
		}
		rows, err := s.q.SearchMessagesLike(ctx, sqlc.SearchMessagesLikeParams{
			UserID:  pgtype.Text{String: query.UserID, Valid: true},
			AgentID: query.AgentID,
			Pattern: []byte("%" + ftsquery.EscapeLike(text) + "%"),
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search messages like: %w", err)
		}
		hits := make([]MessageHit, 0, len(rows))
		for _, row := range rows {
			hits = append(hits, MessageHit{
				ID:                row.ID,
				Content:           truncateUTF8(row.Content, maxContentSnippet),
				CreatedAt:         row.CreatedAt.UTC(),
				SessionID:         row.SessionID,
				ConversationTitle: row.ConversationTitle.String,
			})
		}
		return hits, nil
	}

	rows, err := s.q.SearchMessages(ctx, sqlc.SearchMessagesParams{
		UserID:  pgtype.Text{String: query.UserID, Valid: true},
		AgentID: query.AgentID,
		Match:   match,
		Limit:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	hits := make([]MessageHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, MessageHit{
			ID:                row.ID,
			Content:           searchSnippet(row.Snippet, row.Content),
			Score:             row.Score,
			CreatedAt:         row.CreatedAt.UTC(),
			SessionID:         row.SessionID,
			ConversationTitle: row.ConversationTitle.String,
		})
	}
	return hits, nil
}

func (s *NativePostgresSearch) SearchSummaries(ctx context.Context, query SummaryQuery) ([]SummaryHit, error) {
	limit := normalizeLimit(query.Limit)
	match := ftsquery.BuildMatchQuery(query.Text)
	if match == "" {
		text := strings.TrimSpace(query.Text)
		if text == "" {
			return nil, nil
		}
		rows, err := s.q.SearchSummariesLike(ctx, sqlc.SearchSummariesLikeParams{
			UserID:  pgtype.Text{String: query.UserID, Valid: true},
			AgentID: query.AgentID,
			Pattern: []byte("%" + ftsquery.EscapeLike(text) + "%"),
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search summaries like: %w", err)
		}
		hits := make([]SummaryHit, 0, len(rows))
		for _, row := range rows {
			hits = append(hits, SummaryHit{
				ID:                row.ID,
				Content:           truncateUTF8(row.Content, maxContentSnippet),
				CreatedAt:         row.CreatedAt.UTC(),
				LatestAt:          row.LatestAt,
				SessionID:         row.SessionID,
				ConversationTitle: row.ConversationTitle.String,
			})
		}
		return hits, nil
	}

	rows, err := s.q.SearchSummaries(ctx, sqlc.SearchSummariesParams{
		UserID:  pgtype.Text{String: query.UserID, Valid: true},
		AgentID: query.AgentID,
		Match:   match,
		Limit:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search summaries: %w", err)
	}
	hits := make([]SummaryHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, SummaryHit{
			ID:                row.ID,
			Content:           searchSnippet(row.Snippet, row.Content),
			Score:             row.Score,
			CreatedAt:         row.CreatedAt.UTC(),
			LatestAt:          row.LatestAt,
			SessionID:         row.SessionID,
			ConversationTitle: row.ConversationTitle.String,
		})
	}
	return hits, nil
}

func (s *NativePostgresSearch) SearchArticles(ctx context.Context, query ArticleQuery) ([]ArticleHit, error) {
	limit := normalizeArticleLimit(query.Limit)
	match := ftsquery.BuildMatchQuery(query.Text)
	if match == "" {
		text := strings.TrimSpace(query.Text)
		if text == "" {
			return []ArticleHit{}, nil
		}
		rows, err := s.q.SearchArticlesLike(ctx, sqlc.SearchArticlesLikeParams{
			UserID:  query.UserID,
			Pattern: "%" + ftsquery.EscapeLike(text) + "%",
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search articles like: %w", err)
		}
		hits := make([]ArticleHit, 0, len(rows))
		for _, row := range rows {
			hits = append(hits, ArticleHit{Article: row})
		}
		return hits, nil
	}

	rows, err := s.q.SearchArticles(ctx, sqlc.SearchArticlesParams{
		Match:  match,
		UserID: query.UserID,
		Limit:  int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search articles: %w", err)
	}
	hits := make([]ArticleHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, ArticleHit{
			Article: row.RecallyArticle,
			Snippet: row.Snippet,
			Score:   row.Score,
		})
	}
	return hits, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	return limit
}

func normalizeArticleLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return limit
}

func searchSnippet(snippet, content string) string {
	if snippet != "" {
		return snippet
	}
	return truncateUTF8(content, maxContentSnippet)
}

func truncateUTF8(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
