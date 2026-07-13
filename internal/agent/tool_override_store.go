package agent

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ToolOverrideStore reads persisted tool-visibility overrides for an
// agent+user context. Its Fetch method satisfies ToolOverrideFetcher.
type ToolOverrideStore struct {
	q *sqlc.Queries
}

// NewToolOverrideStore builds a ToolOverrideStore over the given pool.
func NewToolOverrideStore(db *pgxpool.Pool) *ToolOverrideStore {
	return &ToolOverrideStore{q: sqlc.New(db)}
}

// Fetch returns the tool overrides that apply to the given user+agent pair.
func (s *ToolOverrideStore) Fetch(ctx context.Context, userID, agentID string) ([]ToolOverride, error) {
	rows, err := s.q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ToolOverride, 0, len(rows))
	for _, row := range rows {
		out = append(out, ToolOverride{ToolName: row.ToolName, Scope: row.Scope, Enabled: row.Enabled})
	}
	return out, nil
}
