package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return fmt.Errorf("advisory xact lock %q: %w", key, err)
	}
	return nil
}
