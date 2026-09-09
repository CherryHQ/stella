package lcm

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TouchKnowledgeUsage records runtime use for Reflect-owned world facts. The
// SQL query rechecks ownership and status so callers cannot create usage rows
// for manual, deprecated, or non-world facts.
func (p *Provider) TouchKnowledgeUsage(ctx context.Context, userID string, agentID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge usage transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentrun.ValidateTx(ctx, tx); err != nil {
		return err
	}
	qtx := p.q.WithTx(tx)
	for _, factID := range factIDs {
		if err := qtx.TouchKnowledgeUsage(ctx, sqlc.TouchKnowledgeUsageParams{
			FactID:  factID,
			UserID:  userID,
			AgentID: agentID,
		}); err != nil {
			return fmt.Errorf("touch knowledge usage %s: %w", factID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge usage: %w", err)
	}
	return nil
}
