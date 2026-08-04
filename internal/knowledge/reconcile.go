package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	reconciliationBatchSize = 100
	orphanPageSize          = 250
	orphanPagesPerJob       = 4
)

func (s *Service) processReconcileJob(ctx context.Context, job *river.Job[reconcileArgs]) error {
	if !job.Args.OrphansOnly {
		if err := s.reconcileStaleDerivations(ctx); err != nil {
			return err
		}
		if err := s.reconcileTombstones(ctx); err != nil {
			return err
		}
	}
	nextCursor, continueScan, err := s.reconcileOrphanPages(ctx, job.Args.Cursor)
	if err != nil {
		return err
	}
	if !continueScan {
		return nil
	}
	return s.handoffReconcileJob(ctx, job, reconcileArgs{
		Cursor: nextCursor, OrphansOnly: true,
	})
}

func (s *Service) reconcileStaleDerivations(ctx context.Context) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	rows, err := s.q.ListStaleKnowledgeDerivation(ctx, sqlc.ListStaleKnowledgeDerivationParams{
		StaleBefore: time.Now().UTC().Add(-s.staleDerivationAfter),
		Limit:       reconciliationBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list stale knowledge derivations: %w", err)
	}
	for _, row := range rows {
		state, found, err := latestKnowledgeJobState(ctx, client, chunkArgs{}.Kind(), row.ID)
		if err != nil {
			return fmt.Errorf("load knowledge chunk job for %s: %w", row.ID, err)
		}
		if found && slices.Contains(activeKnowledgeJobStates, state) {
			if state != rivertype.JobStateRunning {
				continue
			}
			// The query only returns files whose derivation heartbeat is stale.
			// Since Xberg and every database statement are independently bounded,
			// a still-running row here is a crashed or wedged worker, not healthy work.
			if err := s.cancelActiveChunkJobs(ctx, row.ID); err != nil {
				return fmt.Errorf("cancel stale knowledge derivation %s: %w", row.ID, err)
			}
			// A running River row remains running until its worker observes the
			// cancellation (or River's stuck-job rescuer does). The next bounded
			// reconciliation pass re-enqueues after that durable transition.
			continue
		}
		if found && state == rivertype.JobStateDiscarded {
			if err := s.failStaleGeneration(ctx, row.ID); err != nil {
				return err
			}
			continue
		}
		args := chunkArgs{FileID: row.ID}
		options := args.InsertOpts()
		if _, err := client.Insert(ctx, args, &options); err != nil {
			return fmt.Errorf("re-enqueue stale knowledge derivation %s: %w", row.ID, err)
		}
	}
	return nil
}

func (s *Service) failStaleGeneration(ctx context.Context, fileID string) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin stale knowledge failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockKnowledgeFileLifecycle(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock stale knowledge file: %w", err)
	}
	derivationKey, err := knowledgeDerivationKey(file.RawSha256, file.MediaType)
	if err != nil {
		return err
	}
	set, err := queries.GetKnowledgeChunkSetByDerivation(ctx, sqlc.GetKnowledgeChunkSetByDerivationParams{
		FileID: file.ID, DerivationKey: derivationKey,
	})
	if err == nil {
		locked, lockErr := queries.LockKnowledgeChunkSetLifecycle(ctx, set.ID)
		if lockErr != nil {
			return fmt.Errorf("lock stale KnowledgeChunkSet: %w", lockErr)
		}
		if ChunkSetStatus(locked.Status) == ChunkSetStatusBuilding {
			if _, err := queries.MarkKnowledgeChunkSetFailed(ctx, sqlc.MarkKnowledgeChunkSetFailedParams{
				ErrorMessage: nullableText("Document parsing failed after multiple attempts."),
				ID:           locked.ID,
			}); err != nil {
				return fmt.Errorf("mark stale KnowledgeChunkSet failed: %w", err)
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load stale KnowledgeChunkSet: %w", err)
	}
	if _, err := queries.MarkKnowledgeFileFailedWithoutActiveSet(ctx, sqlc.MarkKnowledgeFileFailedWithoutActiveSetParams{
		ErrorMessage: nullableText("Document parsing failed after multiple attempts."),
		ID:           file.ID,
	}); err != nil {
		return fmt.Errorf("mark stale knowledge file failed: %w", err)
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func (s *Service) reconcileTombstones(ctx context.Context) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	rows, err := s.q.ListKnowledgeTombstone(ctx, reconciliationBatchSize)
	if err != nil {
		return fmt.Errorf("list knowledge tombstones: %w", err)
	}
	for _, row := range rows {
		state, found, err := latestKnowledgeJobState(ctx, client, cleanupArgs{}.Kind(), row.ID)
		if err != nil {
			return fmt.Errorf("load knowledge cleanup job for %s: %w", row.ID, err)
		}
		if found && slices.Contains(activeKnowledgeJobStates, state) {
			if state != rivertype.JobStateRunning ||
				!row.DeletedAt.Valid ||
				row.DeletedAt.Time.After(time.Now().UTC().Add(-s.staleDerivationAfter)) {
				continue
			}
			if err := s.cancelActiveKnowledgeJobs(ctx, cleanupArgs{}.Kind(), row.ID); err != nil {
				return fmt.Errorf("cancel stale knowledge cleanup %s: %w", row.ID, err)
			}
			continue
		}
		args := cleanupArgs{FileID: row.ID}
		options := args.InsertOpts()
		if _, err := client.Insert(ctx, args, &options); err != nil {
			return fmt.Errorf("re-enqueue knowledge cleanup %s: %w", row.ID, err)
		}
	}
	return nil
}

func (s *Service) reconcileOrphanPages(
	ctx context.Context,
	initialCursor string,
) (nextCursor string, continueScan bool, err error) {
	cursor := initialCursor
	cutoff := time.Now().UTC().Add(-s.orphanMinAge)
	for range orphanPagesPerJob {
		page, err := s.rawStore.ListPage(ctx, RawPrefix, cursor, orphanPageSize)
		if err != nil {
			return "", false, fmt.Errorf("list knowledge raw page: %w", err)
		}
		deleted, err := s.reconcileOrphanPage(ctx, page.Objects, cutoff)
		if err != nil {
			return "", false, err
		}
		if deleted {
			// FS cursors are offset-based. Deletion shifts later entries, so restart
			// rather than advancing past objects that moved into this page.
			cursor = ""
			continue
		}
		if page.NextCursor == "" {
			return "", false, nil
		}
		cursor = page.NextCursor
	}
	return cursor, true, nil
}

func (s *Service) reconcileOrphanPage(
	ctx context.Context,
	objects []RawObject,
	cutoff time.Time,
) (bool, error) {
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
		return false, nil
	}
	owners, err := s.q.GetKnowledgeRawOwners(ctx, ids)
	if err != nil {
		return false, fmt.Errorf("resolve knowledge raw ownership: %w", err)
	}
	owned := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		owned[owner.ID] = struct{}{}
	}
	deleted := false
	for _, candidate := range candidates {
		if _, ok := owned[candidate.fileID]; ok {
			continue // both live files and tombstones own their raw until hard delete
		}
		if err := s.rawStore.Delete(ctx, candidate.object.Key); err != nil {
			return deleted, fmt.Errorf("delete orphan knowledge raw: %w", err)
		}
		deleted = true
	}
	return deleted, nil
}

func (s *Service) handoffReconcileJob(
	ctx context.Context,
	job *river.Job[reconcileArgs],
	next reconcileArgs,
) error {
	client := s.riverClient()
	if client == nil {
		return ErrServiceUnavailable
	}
	tx, _, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge reconciliation handoff: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Complete first inside the same transaction so kind-wide uniqueness sees no
	// active predecessor when the continuation is inserted. A rollback restores
	// both operations, so there is still no handoff gap.
	if err := completeRiverJobTx(ctx, tx, job); err != nil {
		return err
	}
	options := next.InsertOpts()
	if _, err := client.InsertTx(ctx, tx, next, &options); err != nil {
		return fmt.Errorf("enqueue knowledge reconciliation continuation: %w", err)
	}
	return commitKnowledgeTransaction(ctx, tx)
}
