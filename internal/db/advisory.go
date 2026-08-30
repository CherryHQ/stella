package db

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/txlock"
)

// AdvisoryXactLock acquires a PostgreSQL transaction-scoped advisory lock keyed
// by an arbitrary string. Concurrent transactions that request the same key run
// serially; transactions with different keys are unaffected. Under SQLite this
// serialization was implicit (single writer); under PostgreSQL writers run in
// parallel, so call this to guard a read-modify-write that must not interleave
// for a given entity. The lock releases automatically when tx commits or rolls
// back — there is no unlock to forget. Prefix keys by domain (e.g. "mem:",
// "group:") so unrelated entities don't share a slot in the 64-bit lock space.
func AdvisoryXactLock(ctx context.Context, tx pgx.Tx, key string) error {
	return txlock.AdvisoryXactLock(ctx, tx, key)
}

// AgentAssignmentLockKey identifies one user's assignment to one Agent. Scope
// transitions that create the assignment and administrative revocations must
// use this exact key, otherwise a revoke can commit between their writes and be
// silently undone by the transition.
func AgentAssignmentLockKey(userID, agentID string) string {
	return txlock.AgentAssignmentLockKey(userID, agentID)
}
