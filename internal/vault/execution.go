package vault

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Keep vault's crypto/test DB interface while fencing its two mutations.
type executionDB struct {
	DB
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func (d executionDB) UpsertVaultEntryByScope(ctx context.Context, arg sqlc.UpsertVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	if d.tx != nil {
		if err := sessionexecution.ValidateTx(ctx, d.tx); err != nil {
			return sqlc.VaultEntry{}, err
		}
		return d.DB.UpsertVaultEntryByScope(ctx, arg)
	}
	return sessionexecution.Write(ctx, d.pool, func(ctx context.Context, q *sqlc.Queries) (sqlc.VaultEntry, error) {
		return q.UpsertVaultEntryByScope(ctx, arg)
	})
}

func (d executionDB) DeleteVaultEntryByScope(ctx context.Context, arg sqlc.DeleteVaultEntryByScopeParams) error {
	if d.tx != nil {
		if err := sessionexecution.ValidateTx(ctx, d.tx); err != nil {
			return err
		}
		return d.DB.DeleteVaultEntryByScope(ctx, arg)
	}
	return sessionexecution.Exec(ctx, d.pool, func(ctx context.Context, q *sqlc.Queries) error { return q.DeleteVaultEntryByScope(ctx, arg) })
}
