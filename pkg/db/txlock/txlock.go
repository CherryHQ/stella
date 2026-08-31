// Package txlock provides transaction-scoped PostgreSQL advisory locks shared
// by persistence packages without creating internal package import cycles.
package txlock

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AdvisoryXactLock acquires a PostgreSQL transaction-scoped advisory lock keyed
// by an arbitrary string. Concurrent transactions that request the same key run
// serially; transactions with different keys are unaffected.
func AdvisoryXactLock(ctx context.Context, tx pgx.Tx, key string) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return fmt.Errorf("advisory xact lock %q: %w", key, err)
	}
	return nil
}

// AgentAssignmentLockKey identifies one user's assignment to one Agent. Scope
// transitions that create the assignment and administrative revocations must
// use this exact key, otherwise a revoke can commit between their writes and be
// silently undone by the transition.
func AgentAssignmentLockKey(userID, agentID string) string {
	return "agent-assignment:" + userID + ":" + agentID
}
