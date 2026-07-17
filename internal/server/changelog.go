package server

import (
	"context"
	"sort"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// readProfileChangelogScope reads one keyset-paginated changelog scope through the
// Profile boundary. The Profile service applies the Agent gate and derives the
// owner tuple from the trusted Authority, so the transport supplies no owner id.
func (s *Server) readProfileChangelogScope(
	ctx context.Context,
	authority authz.Authority,
	agentID string,
	scope string,
	cursor *memory.ChangelogCursor,
	limit int,
) ([]apitypes.ChangelogEntry, error) {
	rows, err := s.profileSvc.ChangelogPage(ctx, authority, agentID, scope, cursor, limit)
	if err != nil {
		return nil, err
	}
	entries := make([]apitypes.ChangelogEntry, len(rows))
	for i, row := range rows {
		entries[i] = memoryChangelogEntryToAPI(row)
	}
	return entries, nil
}

func sortChangelogEntries(entries []apitypes.ChangelogEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].Id > entries[j].Id
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
}
