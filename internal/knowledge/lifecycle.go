package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/authz"
)

const lifecycleBestEffortTimeout = 5 * time.Second

// DeleteManaged commits the tombstone before attempting cancellation or raw
// cleanup. The durable tombstone is the visibility guarantee; cancellation is
// only a best-effort resource optimization.
func (s *Service) DeleteManaged(
	ctx context.Context,
	authority authz.Authority,
	id string,
) error {
	if _, err := s.GetManaged(ctx, authority, id); err != nil {
		return err
	}
	return s.tombstoneManagedFile(ctx, id)
}

func (s *Service) tombstoneManagedFile(ctx context.Context, id string) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge tombstone: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockKnowledgeFileLifecycle(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file for deletion: %w", err)
	}
	if affected, err := queries.TombstoneKnowledgeFile(ctx, id); err != nil {
		return fmt.Errorf("tombstone knowledge file: %w", err)
	} else if affected != 1 {
		return ErrNotFound
	}
	args := cleanupArgs{FileID: id}
	options := args.InsertOpts()
	if _, err := client.InsertTx(ctx, tx, args, &options); err != nil {
		return fmt.Errorf("enqueue knowledge cleanup: %w", err)
	}
	if err := commitKnowledgeTransaction(ctx, tx); err != nil {
		return err
	}

	cancelContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleBestEffortTimeout)
	defer cancel()
	if err := s.cancelActiveChunkJobs(cancelContext, id); err != nil {
		s.logger.Warn("cancel tombstoned knowledge parse jobs", "file_id", id, "error", err)
	}
	return nil
}

func (s *Service) processCleanupJob(ctx context.Context, job *river.Job[cleanupArgs]) error {
	if err := s.cancelActiveChunkJobs(ctx, job.Args.FileID); err != nil {
		s.logger.Warn("cancel knowledge jobs before raw cleanup", "file_id", job.Args.FileID, "error", err)
	}
	file, err := s.q.GetKnowledgeFileLifecycle(ctx, job.Args.FileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.completeCleanupJob(ctx, job)
	}
	if err != nil {
		return fmt.Errorf("load knowledge tombstone for cleanup: %w", err)
	}
	if !file.DeletedAt.Valid {
		return s.completeCleanupJob(ctx, job)
	}
	rawKey, err := RawKey(job.Args.FileID)
	if err != nil {
		return err
	}
	if err := s.rawStore.Delete(ctx, rawKey); err != nil {
		return fmt.Errorf("delete tombstoned knowledge raw: %w", err)
	}

	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge hard deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := queries.LockKnowledgeFileLifecycle(ctx, job.Args.FileID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := completeRiverJobTx(ctx, tx, job); err != nil {
			return err
		}
		return commitKnowledgeTransaction(ctx, tx)
	}
	if err != nil {
		return fmt.Errorf("lock knowledge tombstone for hard deletion: %w", err)
	}
	if locked.DeletedAt.Valid {
		if affected, err := queries.HardDeleteKnowledgeFile(ctx, job.Args.FileID); err != nil {
			return fmt.Errorf("hard-delete knowledge metadata: %w", err)
		} else if affected != 1 {
			return fmt.Errorf("hard-delete knowledge metadata: unexpected rows affected %d", affected)
		}
	}
	if err := completeRiverJobTx(ctx, tx, job); err != nil {
		return err
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func (s *Service) completeCleanupJob(ctx context.Context, job *river.Job[cleanupArgs]) error {
	tx, _, err := s.beginBoundedTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := completeRiverJobTx(ctx, tx, job); err != nil {
		return err
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func (s *Service) cancelActiveChunkJobs(ctx context.Context, fileID string) error {
	return s.cancelActiveKnowledgeJobs(ctx, chunkArgs{}.Kind(), fileID)
}

func (s *Service) cancelActiveKnowledgeJobs(ctx context.Context, kind, fileID string) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	result, err := client.JobList(
		ctx,
		river.NewJobListParams().
			Kinds(kind).
			States(activeKnowledgeJobStates...).
			Where("args ->> 'file_id' = @file_id", river.NamedArgs{"file_id": fileID}).
			OrderBy(river.JobListOrderByID, river.SortOrderDesc).
			First(100),
	)
	if err != nil {
		return fmt.Errorf("list active %s jobs: %w", kind, err)
	}
	var failures []error
	for _, job := range result.Jobs {
		if _, err := client.JobCancel(ctx, job.ID); err != nil {
			failures = append(failures, fmt.Errorf("cancel River job %d: %w", job.ID, err))
		}
	}
	return errors.Join(failures...)
}

func latestKnowledgeJobState(
	ctx context.Context,
	client *river.Client[pgx.Tx],
	kind string,
	fileID string,
) (rivertype.JobState, bool, error) {
	result, err := client.JobList(
		ctx,
		river.NewJobListParams().
			Kinds(kind).
			States(rivertype.JobStates()...).
			Where("args ->> 'file_id' = @file_id", river.NamedArgs{"file_id": fileID}).
			OrderBy(river.JobListOrderByID, river.SortOrderDesc).
			First(1),
	)
	if err != nil {
		return "", false, err
	}
	if len(result.Jobs) == 0 {
		return "", false, nil
	}
	return result.Jobs[0].State, true, nil
}
