package sessionmedia

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	// OrphanSweepQueue isolates the sweep. One worker per node: a sweep is a
	// serialized drainer and two concurrent rounds would only fight for rows.
	OrphanSweepQueue = "stella_session_media_sweep"
	// orphanSweepInterval is slow on purpose. Orphans cost storage, not
	// correctness, and every round scans the group message table.
	orphanSweepInterval = 6 * time.Hour
	// orphanSweepBatch bounds one round so a large backlog drains over several
	// rounds instead of holding one long transaction.
	orphanSweepBatch = 500
	// maxRoundsPerSweep bounds the drain. A firing that fills every round stops
	// and lets the next tick continue: the backlog is recomputed each time.
	maxRoundsPerSweep = 20
)

var errNoRiverClient = errors.New("sessionmedia: StartOrphanSweep before BindRiverClient")

// sweepOnce deletes one round of unreferenced media rows and then their blobs.
// The database is the authority: a row that survived the delete keeps its
// object, and a blob whose row is gone is unreachable whether or not this
// delete succeeds, so a blob failure is logged and the round still counts.
func (p *Pipeline) sweepOnce(ctx context.Context) (int, error) {
	rows, err := p.media.q.DeleteOrphanMedia(ctx, orphanSweepBatch)
	if err != nil {
		return 0, fmt.Errorf("delete orphan session media: %w", err)
	}
	for _, row := range rows {
		owner, digest, err := sweepTarget(row)
		if err != nil {
			slog.Warn("sessionmedia: orphan row has no addressable object", "error", err)
			continue
		}
		if err := p.media.media.DeleteSessionMedia(ctx, owner, digest); err != nil {
			slog.Warn("sessionmedia: orphan blob not deleted", "owner_kind", owner.Kind, "owner_id", owner.ID, "error", err)
		}
	}
	return len(rows), nil
}

func sweepTarget(row sqlc.DeleteOrphanMediaRow) (Owner, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if len(row.Sha256) != sha256.Size {
		return Owner{}, digest, fmt.Errorf("orphan media digest is %d bytes", len(row.Sha256))
	}
	copy(digest[:], row.Sha256)
	id, err := uuid.Parse(row.OwnerID.String)
	if err != nil {
		return Owner{}, digest, fmt.Errorf("orphan media owner id: %w", err)
	}
	switch asset.OwnerKind(row.OwnerKind.String) {
	case asset.OwnerUser:
		return UserOwner(id), digest, nil
	case asset.OwnerGroup:
		return GroupOwner(id), digest, nil
	default:
		return Owner{}, digest, fmt.Errorf("orphan media owner kind %q", row.OwnerKind.String)
	}
}

// orphanSweepArgs carries nothing: the backlog is read from the database at
// work time, so a firing enqueued hours ago still does today's work.
type orphanSweepArgs struct{}

func (orphanSweepArgs) Kind() string { return "stella_session_media_sweep" }

type orphanSweepWorker struct {
	river.WorkerDefaults[orphanSweepArgs]
	pipeline *Pipeline
}

// Work drains up to maxRoundsPerSweep rounds. A failure is logged and retried on
// the next tick rather than surfaced to River: nothing downstream waits on a
// sweep, and a permanently failing job would only accumulate retries.
func (w *orphanSweepWorker) Work(ctx context.Context, _ *river.Job[orphanSweepArgs]) error {
	for range maxRoundsPerSweep {
		deleted, err := w.pipeline.sweepOnce(ctx)
		if err != nil {
			slog.Warn("sessionmedia: orphan sweep round failed, retrying next tick", "error", err)
			return nil
		}
		if deleted < orphanSweepBatch {
			break
		}
	}
	return nil
}

// OrphanSweepQueueConfig returns the queue name and per-node worker config for
// the composition root assembling the shared working client.
func (p *Pipeline) OrphanSweepQueueConfig() (string, river.QueueConfig) {
	return OrphanSweepQueue, river.QueueConfig{MaxWorkers: 1}
}

// RegisterRiverWorker registers the sweep worker into the shared workers bundle.
// Call before building the client (composition root).
func (p *Pipeline) RegisterRiverWorker(workers *river.Workers) {
	river.AddWorker(workers, &orphanSweepWorker{pipeline: p})
}

// BindRiverClient injects the shared working River client. One-shot pre-start
// bind: it rejects a nil client, a second bind, and any bind after the sweep
// has started.
func (p *Pipeline) BindRiverClient(c *river.Client[pgx.Tx]) error {
	if c == nil {
		return errors.New("sessionmedia: BindRiverClient requires a non-nil client")
	}
	p.riverMu.Lock()
	defer p.riverMu.Unlock()
	if p.sweepStarted {
		return errors.New("sessionmedia: BindRiverClient after StartOrphanSweep")
	}
	if p.river != nil {
		return errors.New("sessionmedia: river client already bound")
	}
	p.river = c
	return nil
}

// StartOrphanSweep registers the sweep as a single-leader River periodic job.
// RunOnStart makes a restart the way an operator forces a sweep; ByState
// uniqueness keeps at most one in flight cluster-wide.
func (p *Pipeline) StartOrphanSweep() (rivertype.PeriodicJobHandle, error) {
	p.riverMu.Lock()
	if p.river == nil {
		p.riverMu.Unlock()
		return 0, errNoRiverClient
	}
	p.sweepStarted = true
	client := p.river
	p.riverMu.Unlock()
	return client.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(orphanSweepInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return orphanSweepArgs{}, &river.InsertOpts{
				Queue: OrphanSweepQueue,
				UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
					rivertype.JobStateAvailable,
					rivertype.JobStatePending,
					rivertype.JobStateRunning,
					rivertype.JobStateScheduled,
					rivertype.JobStateRetryable,
				}},
			}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	)), nil
}

// StopOrphanSweep removes the periodic so no further firings are enqueued.
func (p *Pipeline) StopOrphanSweep(handle rivertype.PeriodicJobHandle) {
	p.riverMu.Lock()
	client := p.river
	p.riverMu.Unlock()
	if client == nil {
		return
	}
	client.PeriodicJobs().Remove(handle)
}
