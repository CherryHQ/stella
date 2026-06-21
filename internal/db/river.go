package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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

const (
	// riverJobTimeout disables River's per-job timeout (default 1m). Both the
	// scheduler and goal queues execute agent runs that routinely exceed a minute;
	// graceful shutdown still cancels in-flight work via SoftStopTimeout.
	riverJobTimeout = -1
	// riverSoftStopTimeout bounds how long Stop waits for in-flight jobs to drain
	// before cancelling their work contexts.
	riverSoftStopTimeout = 30 * time.Second
	// riverRescueStuckJobsAfter keeps River from marking a legitimately long agent
	// run as stuck (default 1h). App-level guards (scheduler's tryStartJobRun, the
	// goal lease/reaper) are the real backstops; this only keeps River's own job
	// state from going misleadingly stuck/discarded under expected run durations.
	riverRescueStuckJobsAfter = 24 * time.Hour
)

// NewWorkingRiverClient builds a WORKING (electable) River client that works the
// given queues with the given workers. River elects a single leader per database to
// run leader-only maintenance — including the periodic-job enqueuer, which only
// enqueues the periodic jobs registered on the LEADER client. A second electable
// client could win leadership and silently starve another client's periodic jobs
// (e.g. the scheduler's cron).
//
// INVARIANT: exactly ONE electable client per database. That is a process+database
// scoped property and cannot be enforced here — the test suite legitimately builds
// many across separate databases in one process. The composition root (cmd/stellad
// buildSharedRiverClient) is the single production enforcement point: it assembles
// one client from every subsystem's queues + workers and injects it back via
// SetRiverClient, so no subsystem constructs its own electable client. A new
// electable construction site is a bug; route subsystems through the composition
// root instead. An insert-only client (no queues, never Started) must use
// river.NewClient directly, not this constructor — which is what the guard below
// enforces: a working client must actually have queues and workers to work.
func NewWorkingRiverClient(pool *pgxpool.Pool, queues map[string]river.QueueConfig, workers *river.Workers, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	if len(queues) == 0 || workers == nil {
		return nil, fmt.Errorf("working river client requires queues and workers (got %d queues, workers set=%t); use river.NewClient directly for an insert-only client", len(queues), workers != nil)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:               logger,
		Queues:               queues,
		Workers:              workers,
		JobTimeout:           riverJobTimeout,
		SoftStopTimeout:      riverSoftStopTimeout,
		RescueStuckJobsAfter: riverRescueStuckJobsAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("create working river client: %w", err)
	}
	return client, nil
}
