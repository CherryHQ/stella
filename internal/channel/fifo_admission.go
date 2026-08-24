package channel

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// createChannelBindingFIFO serializes quota accounting and revision allocation
// in two PostgreSQL statements behind one deployment-global advisory lock;
// shard the lock per principal if ingest throughput ever matters.
// The separate lock statement is essential under
// READ COMMITTED: a statement that waits for an advisory lock does not refresh
// the snapshot it took before waiting.
func createChannelBindingFIFO(ctx context.Context, db *pgxpool.Pool, params sqlc.CreateChannelBindingFIFOParams) (sqlc.ChannelBindingFifo, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return sqlc.ChannelBindingFifo{}, fmt.Errorf("begin channel FIFO admission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	row, err := createChannelBindingFIFOWithQueries(ctx, q, params)
	if err != nil {
		return sqlc.ChannelBindingFifo{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.ChannelBindingFifo{}, fmt.Errorf("commit channel FIFO admission: %w", err)
	}
	return row, nil
}

// createChannelBindingFIFOWithQueries is used when GroupRoute already owns the
// transaction that atomically materializes responder and FIFO rows.
func createChannelBindingFIFOWithQueries(ctx context.Context, q *sqlc.Queries, params sqlc.CreateChannelBindingFIFOParams) (sqlc.ChannelBindingFifo, error) {
	if err := q.LockChannelBindingFIFOAdmission(ctx); err != nil {
		return sqlc.ChannelBindingFifo{}, fmt.Errorf("lock channel FIFO admission: %w", err)
	}
	return q.CreateChannelBindingFIFO(ctx, params)
}
