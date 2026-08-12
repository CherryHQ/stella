package library

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const LibraryQueue = "stella_library"

var activeLibraryJobStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
	rivertype.JobStateRetryable,
}

// chunkArgs carries only durable identity so retries and process restarts reload
// every derivation input from committed state.
type chunkArgs struct {
	FileID string `json:"file_id" river:"unique"`
}

func (chunkArgs) Kind() string { return "stella_library_chunk" }

func (chunkArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       LibraryQueue,
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: append([]rivertype.JobState(nil), activeLibraryJobStates...),
		},
	}
}

type cleanupArgs struct {
	FileID string `json:"file_id" river:"unique"`
}

func (cleanupArgs) Kind() string { return "stella_library_cleanup" }

func (cleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       LibraryQueue,
		MaxAttempts: 10,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: append([]rivertype.JobState(nil), activeLibraryJobStates...),
		},
	}
}

// reconcileArgs has no durable scan state. Every bounded periodic pass starts
// at the RawStore head and relies on idempotent lifecycle operations.
type reconcileArgs struct{}

func (reconcileArgs) Kind() string { return "stella_library_reconcile" }

func (reconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       LibraryQueue,
		MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{
			ByState: append([]rivertype.JobState(nil), activeLibraryJobStates...),
		},
	}
}

type chunkWorker struct {
	river.WorkerDefaults[chunkArgs]
	service *Service
}

func (w *chunkWorker) Work(ctx context.Context, job *river.Job[chunkArgs]) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("library chunk worker panicked: %v", value)
		}
	}()
	return w.service.processChunkJob(ctx, job)
}

type cleanupWorker struct {
	river.WorkerDefaults[cleanupArgs]
	service *Service
}

func (w *cleanupWorker) Work(ctx context.Context, job *river.Job[cleanupArgs]) error {
	return w.service.processCleanupJob(ctx, job)
}

type reconcileWorker struct {
	river.WorkerDefaults[reconcileArgs]
	service *Service
	logger  *slog.Logger
}

func (w *reconcileWorker) Work(ctx context.Context, job *river.Job[reconcileArgs]) error {
	if err := w.service.processReconcileJob(ctx, job); err != nil {
		w.logger.Warn("library reconciliation failed", "error", err)
		return err
	}
	return nil
}

// QueueConfig returns the dedicated per-node parser/maintenance concurrency.
func (s *Service) QueueConfig() (string, river.QueueConfig) {
	return LibraryQueue, river.QueueConfig{MaxWorkers: s.maxWorkers}
}

// RegisterRiverWorkers contributes all internal Library workers to the one
// process-wide River client. No management or Agent surface is registered here.
func (s *Service) RegisterRiverWorkers(workers *river.Workers) {
	river.AddWorker(workers, &chunkWorker{service: s})
	river.AddWorker(workers, &cleanupWorker{service: s})
	river.AddWorker(workers, &reconcileWorker{
		service: s,
		logger:  s.logger.With("worker", "reconcile"),
	})
}

// StartReconciliation registers one leader-elected bounded repair chain.
func (s *Service) StartReconciliation() (rivertype.PeriodicJobHandle, error) {
	s.mu.Lock()
	client := s.river
	if client == nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("library: StartReconciliation before BindRiverClient")
	}
	if s.started {
		s.mu.Unlock()
		return 0, fmt.Errorf("library: reconciliation already started")
	}
	s.started = true
	s.mu.Unlock()

	handle := client.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(s.reconciliationInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			args := reconcileArgs{}
			options := args.InsertOpts()
			return args, &options
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))
	return handle, nil
}

func (s *Service) StopReconciliation(handle rivertype.PeriodicJobHandle) {
	client := s.riverClient()
	if client != nil {
		client.PeriodicJobs().Remove(handle)
	}
}

func completeRiverJobTx[T river.JobArgs](
	ctx context.Context,
	tx pgx.Tx,
	job *river.Job[T],
) error {
	updated, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
	if err != nil {
		return fmt.Errorf("complete %s River job: %w", job.Args.Kind(), err)
	}
	// River returns the durable row even when another worker already finalized
	// it. Refuse to commit Library terminal writes unless this transaction won
	// the running-to-completed transition.
	if updated.State != rivertype.JobStateCompleted {
		return fmt.Errorf(
			"library %s job %d was finalized elsewhere (%s)",
			job.Args.Kind(),
			job.ID,
			updated.State,
		)
	}
	return nil
}
