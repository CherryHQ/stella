package lcm

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/sessionexecution"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TouchKnowledgeUsage records runtime use for Reflect-owned world facts. The
// SQL query rechecks ownership and status so callers cannot create usage rows
// for manual, deprecated, or non-world facts.
func (p *Provider) TouchKnowledgeUsage(ctx context.Context, userID string, agentID string, factIDs []string) error {
	for _, factID := range factIDs {
		if err := sessionexecution.Exec(ctx, p.db, func(ctx context.Context, q *sqlc.Queries) error {
			return q.TouchKnowledgeUsage(ctx, sqlc.TouchKnowledgeUsageParams{
				FactID:  factID,
				UserID:  userID,
				AgentID: agentID,
			})
		}); err != nil {
			return fmt.Errorf("touch knowledge usage %s: %w", factID, err)
		}
	}
	return nil
}
