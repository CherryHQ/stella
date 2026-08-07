package library

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	reconciliationBatchSize = 100
	orphanPageSize          = 250
	orphanPagesPerJob       = 4
)

func (s *Service) processReconcileJob(ctx context.Context, _ *river.Job[reconcileArgs]) error {
	if err := s.reconcileStaleDerivations(ctx); err != nil {
		return err
	}
	if err := s.reconcileTombstones(ctx); err != nil {
		return err
	}
	return s.reconcileOrphanPages(ctx)
}

func (s *Service) reconcileStaleDerivations(ctx context.Context) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	staleBefore := time.Now().UTC().Add(-s.staleDerivationAfter)
	rows, err := s.q.ListStaleLibraryDerivation(ctx, sqlc.ListStaleLibraryDerivationParams{
		StaleBefore: staleBefore,
		Limit:       reconciliationBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list stale library derivations: %w", err)
	}
	for _, row := range rows {
		job, found, err := latestLibraryJob(ctx, client, chunkArgs{}.Kind(), row.ID)
		if err != nil {
			return fmt.Errorf("load library chunk job for %s: %w", row.ID, err)
		}
		if found && slices.Contains(activeLibraryJobStates, job.State) {
			if job.State != rivertype.JobStateRunning {
				continue
			}
			if job.AttemptedAt == nil || !job.AttemptedAt.Before(staleBefore) {
				continue
			}
			// The query only returns files whose derivation heartbeat is stale. The
			// in-process parser and every database statement are bounded, so this is
			// a crashed or wedged worker rather than healthy work.
			if err := finalizeStaleRunningLibraryJob(ctx, client, job); err != nil {
				return fmt.Errorf("cancel stale library derivation %s: %w", row.ID, err)
			}
			// The stale row no longer occupies River uniqueness, so the idempotent
			// replacement below can take over without changing the global rescuer.
		}
		if found && job.State == rivertype.JobStateDiscarded {
			if err := s.failStaleGeneration(ctx, row.ID); err != nil {
				return err
			}
			continue
		}
		args := chunkArgs{FileID: row.ID}
		options := args.InsertOpts()
		if _, err := client.Insert(ctx, args, &options); err != nil {
			return fmt.Errorf("re-enqueue stale library derivation %s: %w", row.ID, err)
		}
	}
	return nil
}

func (s *Service) failStaleGeneration(ctx context.Context, fileID string) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin stale library failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockLibraryFileLifecycle(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock stale library file: %w", err)
	}
	derivationKey, err := libraryDerivationKey(file.RawSha256, file.MediaType, s.parserProfile)
	if err != nil {
		return err
	}
	set, err := queries.GetLibraryChunkSetByDerivation(ctx, sqlc.GetLibraryChunkSetByDerivationParams{
		FileID: file.ID, DerivationKey: derivationKey,
	})
	if err == nil {
		locked, lockErr := queries.LockLibraryChunkSetLifecycle(ctx, set.ID)
		if lockErr != nil {
			return fmt.Errorf("lock stale LibraryChunkSet: %w", lockErr)
		}
		if ChunkSetStatus(locked.Status) == ChunkSetStatusBuilding {
			if _, err := queries.MarkLibraryChunkSetFailed(ctx, sqlc.MarkLibraryChunkSetFailedParams{
				ErrorMessage: nullableText("Document parsing failed after multiple attempts."),
				ID:           locked.ID,
			}); err != nil {
				return fmt.Errorf("mark stale LibraryChunkSet failed: %w", err)
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load stale LibraryChunkSet: %w", err)
	}
	if _, err := queries.MarkLibraryFileFailedWithoutActiveSet(ctx, sqlc.MarkLibraryFileFailedWithoutActiveSetParams{
		ErrorMessage: nullableText("Document parsing failed after multiple attempts."),
		ID:           file.ID,
	}); err != nil {
		return fmt.Errorf("mark stale library file failed: %w", err)
	}
	return commitLibraryTransaction(ctx, tx)
}

func (s *Service) reconcileTombstones(ctx context.Context) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	rows, err := s.q.ListLibraryTombstone(ctx, reconciliationBatchSize)
	if err != nil {
		return fmt.Errorf("list library tombstones: %w", err)
	}
	staleBefore := time.Now().UTC().Add(-s.staleDerivationAfter)
	for _, row := range rows {
		job, found, err := latestLibraryJob(ctx, client, cleanupArgs{}.Kind(), row.ID)
		if err != nil {
			return fmt.Errorf("load library cleanup job for %s: %w", row.ID, err)
		}
		if found && slices.Contains(activeLibraryJobStates, job.State) {
			if job.State != rivertype.JobStateRunning ||
				!row.DeletedAt.Valid ||
				row.DeletedAt.Time.After(staleBefore) ||
				job.AttemptedAt == nil ||
				!job.AttemptedAt.Before(staleBefore) {
				continue
			}
			if err := finalizeStaleRunningLibraryJob(ctx, client, job); err != nil {
				return fmt.Errorf("cancel stale library cleanup %s: %w", row.ID, err)
			}
		}
		args := cleanupArgs{FileID: row.ID}
		options := args.InsertOpts()
		if _, err := client.Insert(ctx, args, &options); err != nil {
			return fmt.Errorf("re-enqueue library cleanup %s: %w", row.ID, err)
		}
	}
	return nil
}

func finalizeStaleRunningLibraryJob(
	ctx context.Context,
	client *river.Client[pgx.Tx],
	job *rivertype.JobRow,
) error {
	if job == nil || job.State != rivertype.JobStateRunning {
		return fmt.Errorf("library stale-job finalization requires a running job")
	}
	if _, err := client.JobCancel(ctx, job.ID); err != nil {
		return fmt.Errorf("request River job %d cancellation: %w", job.ID, err)
	}
	now := time.Now().UTC()
	// River's executor is an internal API. This deliberate coupling is kept
	// narrow so Library can release a stale running job from uniqueness before
	// inserting its replacement, without changing River's global rescue policy.
	params := riverdriver.JobSetStateCancelled(job.ID, now, nil, nil)
	rows, err := client.Driver().GetExecutor().JobSetStateIfRunningMany(ctx, &riverdriver.JobSetStateIfRunningManyParams{
		ID:              []int64{params.ID},
		Attempt:         []*int{params.Attempt},
		ErrData:         [][]byte{params.ErrData},
		FinalizedAt:     []*time.Time{params.FinalizedAt},
		MetadataDoMerge: []bool{params.MetadataDoMerge},
		MetadataUpdates: [][]byte{params.MetadataUpdates},
		Now:             &now,
		ScheduledAt:     []*time.Time{params.ScheduledAt},
		State:           []rivertype.JobState{params.State},
	})
	if err != nil {
		return fmt.Errorf("finalize stale River job %d: %w", job.ID, err)
	}
	if len(rows) != 1 || rows[0].State == rivertype.JobStateRunning {
		return fmt.Errorf("stale River job %d remained running", job.ID)
	}
	return nil
}

func (s *Service) reconcileOrphanPages(ctx context.Context) error {
	// Each periodic run starts from the canonical-key head and scans a fixed
	// number of pages. This bounds one maintenance job without persisting a
	// continuation protocol; later runs safely repeat the same idempotent work.
	cursor := ""
	cutoff := time.Now().UTC().Add(-s.orphanMinAge)
	for range orphanPagesPerJob {
		page, err := s.rawStore.ListPage(ctx, RawPrefix, cursor, orphanPageSize)
		if err != nil {
			return fmt.Errorf("list library raw page: %w", err)
		}
		if err := s.reconcileOrphanPage(ctx, page.Objects, cutoff); err != nil {
			return err
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
	return nil
}

func (s *Service) reconcileOrphanPage(
	ctx context.Context,
	objects []RawObject,
	cutoff time.Time,
) error {
	type candidate struct {
		object RawObject
		fileID string
	}
	candidates := make([]candidate, 0, len(objects))
	ids := make([]string, 0, len(objects))
	for _, object := range objects {
		if !object.LastModified.Before(cutoff) {
			continue
		}
		fileID, err := FileIDFromRawKey(object.Key)
		if err != nil {
			continue // malformed or foreign keys are retained fail-closed
		}
		candidates = append(candidates, candidate{object: object, fileID: fileID})
		ids = append(ids, fileID)
	}
	if len(candidates) == 0 {
		return nil
	}
	owners, err := s.q.GetLibraryRawOwners(ctx, ids)
	if err != nil {
		return fmt.Errorf("resolve library raw ownership: %w", err)
	}
	owned := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		owned[owner.ID] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, ok := owned[candidate.fileID]; ok {
			continue // both live files and tombstones own their raw until hard delete
		}
		if err := s.rawStore.Delete(ctx, candidate.object.Key); err != nil {
			return fmt.Errorf("delete orphan library raw: %w", err)
		}
	}
	return nil
}
