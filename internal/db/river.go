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
	"github.com/riverqueue/river/rivertype"
)

// riverErrorHandler surfaces job errors and panics that River's default logger
// would otherwise bury, with enough structure (kind, id, attempt) to find the
// failing job. It does NOT override River's retry/discard decision (returns nil);
// the goal lease reaper and scheduler guards still own recovery. Visibility
// matters most for MaxAttempts=1 jobs (goal attempts), which go straight to
// `discarded` on a single error with no retry and no other callback.
type riverErrorHandler struct{ log *slog.Logger }

func (h riverErrorHandler) HandleError(_ context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	h.log.Error("river job errored",
		"kind", job.Kind, "job_id", job.ID, "queue", job.Queue,
		"attempt", job.Attempt, "max_attempts", job.MaxAttempts, "err", err)
	return nil
}

func (h riverErrorHandler) HandlePanic(_ context.Context, job *rivertype.JobRow, panicVal any, trace string) *river.ErrorHandlerResult {
	h.log.Error("river job panicked",
		"kind", job.Kind, "job_id", job.ID, "queue", job.Queue,
		"attempt", job.Attempt, "panic", fmt.Sprintf("%v", panicVal), "trace", trace)
	return nil
}

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
	// DefaultRiverSoftStopTimeout is the fallback drain budget for the self-contained
	// scheduler client (default/test path). Production injects a value parsed from
	// STELLA_RIVER_SOFT_STOP_TIMEOUT via the composition root; this package reads no
	// env itself.
	DefaultRiverSoftStopTimeout = 120 * time.Second
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
// BindRiverClient, so no subsystem constructs its own electable client. A new
// electable construction site is a bug; route subsystems through the composition
// root instead. An insert-only client (no queues, never Started) must use
// river.NewClient directly, not this constructor — which is what the guard below
// enforces: a working client must actually have queues and workers to work.
//
// softStopTimeout bounds how long Stop waits for in-flight jobs to drain before
// River cancels their work contexts (escalating to a hard stop). It is injected
// rather than read from the environment here so this package stays env-free; the
// composition root parses STELLA_RIVER_SOFT_STOP_TIMEOUT and passes it in.
func NewWorkingRiverClient(pool *pgxpool.Pool, queues map[string]river.QueueConfig, workers *river.Workers, logger *slog.Logger, softStopTimeout time.Duration) (*river.Client[pgx.Tx], error) {
	if len(queues) == 0 || workers == nil {
		return nil, fmt.Errorf("working river client requires queues and workers (got %d queues, workers set=%t); use river.NewClient directly for an insert-only client", len(queues), workers != nil)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:               logger,
		Queues:               queues,
		Workers:              workers,
		JobTimeout:           riverJobTimeout,
		SoftStopTimeout:      softStopTimeout,
		RescueStuckJobsAfter: riverRescueStuckJobsAfter,
		ErrorHandler:         riverErrorHandler{log: logger},
	})
	if err != nil {
		return nil, fmt.Errorf("create working river client: %w", err)
	}
	return client, nil
}
