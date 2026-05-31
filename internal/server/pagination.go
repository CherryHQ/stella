package server

import "fmt"

const (
	defaultPageSize = 20
	maxPageSize     = 500
)

// parsePageParams resolves AIP-158 page_size / page_token query parameters into
// a (limit, offset) pair. limit defaults to 20 and is bounded to [1, 500];
// page_size outside that range or a malformed token yields an error suitable
// for a 400 response. A nil page_size uses the default; a nil token starts at 0.
func parsePageParams(pageSize *int, pageToken *string) (limit, offset int, err error) {
	limit = defaultPageSize
	if pageSize != nil {
		if *pageSize < 1 || *pageSize > maxPageSize {
			return 0, 0, fmt.Errorf("page_size must be between 1 and %d", maxPageSize)
		}
		limit = *pageSize
	}
	if pageToken != nil {
		offset, err = decodeOffsetToken(*pageToken)
		if err != nil {
			return 0, 0, err
		}
	}
	return limit, offset, nil
}

// nextPageTokenForRows computes the next_page_token for a DB-backed list that
// fetched limit+1 rows to detect a further page. It returns the token (empty
// when no more rows) and the rows trimmed back to limit.
func nextPageTokenForRows[T any](rows []T, limit, offset int) (page []T, nextToken string) {
	if len(rows) > limit {
		return rows[:limit], encodeOffsetToken(offset + limit)
	}
	return rows, ""
}
