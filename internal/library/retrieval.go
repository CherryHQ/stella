package library

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	MaxSearchQueryRunes = 500
	DefaultSearchLimit  = 5
	MaxSearchLimit      = 10
)

// Search performs one permission-aware BM25 query for a trusted delegated
// Agent. The exact four-scope union is enforced in SQL, before any candidate can
// cross into Go.
func (s *Service) Search(
	ctx context.Context,
	authority authz.Authority,
	query string,
	limit int,
) ([]SearchHit, error) {
	if s == nil || s.q == nil {
		return nil, ErrServiceUnavailable
	}
	if !authority.Valid() || authority.Kind() != authz.ActorAgent {
		return nil, ErrForbidden
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidSearch)
	}
	if utf8.RuneCountInString(query) > MaxSearchQueryRunes {
		return nil, fmt.Errorf("%w: query must not exceed %d Unicode characters", ErrInvalidSearch, MaxSearchQueryRunes)
	}
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	if limit < 1 || limit > MaxSearchLimit {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidSearch, MaxSearchLimit)
	}

	searchCtx, cancel := context.WithTimeout(ctx, s.databaseStatementTimeout)
	defer cancel()
	rows, err := s.q.SearchLibraryChunks(searchCtx, sqlc.SearchLibraryChunksParams{
		Match:   query,
		UserID:  nullableText(string(authority.UserID())),
		AgentID: nullableText(string(authority.AgentID())),
		Limit:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search Library chunks: %w", err)
	}

	hits := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		locator, err := publicSearchLocator(row.Locator)
		if err != nil {
			return nil, fmt.Errorf("decode Library search locator: %w", err)
		}
		hits = append(hits, SearchHit{
			FileName: row.FileName,
			Locator:  locator,
			Content:  row.Content,
		})
	}
	return hits, nil
}

// publicSearchLocator removes byte offsets, which are internal parser details
// rather than stable, user-facing citation coordinates.
func publicSearchLocator(raw json.RawMessage) (*SearchLocator, error) {
	var stored ChunkLocator
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	if stored.FirstPage == nil && stored.LastPage == nil && len(stored.HeadingPath) == 0 {
		return nil, nil
	}
	if stored.FirstPage != nil && stored.LastPage != nil && *stored.LastPage < *stored.FirstPage {
		return nil, fmt.Errorf("last page precedes first page")
	}
	return &SearchLocator{
		FirstPage:   stored.FirstPage,
		LastPage:    stored.LastPage,
		HeadingPath: append([]string(nil), stored.HeadingPath...),
	}, nil
}
