package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The send receipt is a run write; SMTP itself cannot be rolled back by a lease.
type EmailQueries struct {
	*sqlc.Queries
	db *pgxpool.Pool
}

func NewEmailQueries(db *pgxpool.Pool) *EmailQueries {
	return &EmailQueries{Queries: sqlc.New(db), db: db}
}

func (q EmailQueries) DeleteExpiredEmailSendDedup(ctx context.Context) error {
	return sessionexecution.Exec(ctx, q.db, func(ctx context.Context, q *sqlc.Queries) error { return q.DeleteExpiredEmailSendDedup(ctx) })
}

func (q EmailQueries) CreateEmailSendDedup(ctx context.Context, arg sqlc.CreateEmailSendDedupParams) (sqlc.EmailSendDedup, error) {
	return sessionexecution.Write(ctx, q.db, func(ctx context.Context, q *sqlc.Queries) (sqlc.EmailSendDedup, error) {
		return q.CreateEmailSendDedup(ctx, arg)
	})
}

func (q EmailQueries) DeleteEmailSendDedup(ctx context.Context, arg sqlc.DeleteEmailSendDedupParams) error {
	return sessionexecution.Exec(ctx, q.db, func(ctx context.Context, q *sqlc.Queries) error { return q.DeleteEmailSendDedup(ctx, arg) })
}
