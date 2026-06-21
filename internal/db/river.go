package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// migrateRiver applies River's own schema migrations — the river_* tables that
// back its durable job queue. River owns and versions these tables itself, so
// they live outside the app's goose migrations/ directory; rivermigrate is their
// source of truth.
//
// Called from migrate() while the cross-process advisory lock is held, so
// concurrent stellad instances serialize here exactly as they do for the app's
// own migrations.
func migrateRiver(ctx context.Context, pool *pgxpool.Pool) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrate up: %w", err)
	}
	return nil
}

// NewRiverClient returns an insert-only River client bound to the shared pool:
// it can enqueue jobs but works none, because it configures neither Queues nor
// Workers. This is the Phase 0 substrate — the durable-queue plumbing stands up
// and its schema is migrated, but nothing drives off it yet. Phase 1 supplies
// Queues + Workers (and Start()s the client) to actually run jobs.
func NewRiverClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	return client, nil
}
