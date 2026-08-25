package agentrun

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// WriteTx commits one Run-owned source-domain mutation only while the immutable
// AgentRun owner is still current. Contexts without a Guard use the same
// transaction path but have no execution lease to validate.
func WriteTx(ctx context.Context, db *pgxpool.Pool, write func(*sqlc.Queries) error) error {
	_, err := WriteTxValue(ctx, db, func(q *sqlc.Queries) (struct{}, error) {
		return struct{}{}, write(q)
	})
	return err
}

// WriteTxValue is WriteTx for a mutation that returns a row or affected count.
func WriteTxValue[T any](ctx context.Context, db *pgxpool.Pool, write func(*sqlc.Queries) (T, error)) (T, error) {
	var zero T
	if db == nil {
		return zero, fmt.Errorf("AgentRun guarded write database is not configured")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ValidateTx(ctx, tx); err != nil {
		return zero, err
	}
	value, err := write(sqlc.New(tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return value, nil
}
