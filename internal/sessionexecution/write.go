package sessionexecution

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Begin reuses the caller's existing transaction boundary and lock order.
func Begin(ctx context.Context, db *pgxpool.Pool) (pgx.Tx, error) {
	if FromContext(ctx) != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, OperationTimeout)
		defer cancel()
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateTx(ctx, tx); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), OperationTimeout)
		defer cancel()
		_ = tx.Rollback(cleanup)
		return nil, err
	}
	return tx, nil
}

// Write gives a previously autocommit mutation a short guarded transaction.
// Unmarked user APIs and independently accepted jobs retain their own authority.
func Write[T any](ctx context.Context, db *pgxpool.Pool, fn func(context.Context, *sqlc.Queries) (T, error)) (T, error) {
	var zero T
	if err := Check(ctx); err != nil {
		return zero, err
	}
	if FromContext(ctx) == nil {
		return fn(ctx, sqlc.New(db))
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	tx, err := Begin(ctx, db)
	if err != nil {
		return zero, err
	}
	defer rollback(tx)
	value, err := fn(ctx, sqlc.New(tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return value, nil
}

func Exec(ctx context.Context, db *pgxpool.Pool, fn func(context.Context, *sqlc.Queries) error) error {
	_, err := Write(ctx, db, func(ctx context.Context, q *sqlc.Queries) (struct{}, error) { return struct{}{}, fn(ctx, q) })
	return err
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), OperationTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}
