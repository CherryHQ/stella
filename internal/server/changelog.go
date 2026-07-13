package server

import (
	"context"
	"sort"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/memory"
)

func (s *Server) readProfileChangelogScope(
	ctx context.Context,
	userID string,
	agentID string,
	scope string,
	cursor *memory.ChangelogCursor,
	limit int,
) ([]apitypes.ChangelogEntry, error) {
	rows, err := s.memoryManagement.ReadChangelogPage(ctx, userID, agentID, scope, cursor, limit)
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
