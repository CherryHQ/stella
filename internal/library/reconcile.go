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
	orphanCursorSettingKey  = "library_orphan_gc_cursor"
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
	mediaTypes := SupportedMediaTypes()
	processorKeys := make([]string, 0, len(mediaTypes))
	availableMediaTypes := make([]string, 0, len(mediaTypes))
	profiles := make(map[string]string, len(mediaTypes))
	for _, mediaType := range mediaTypes {
		processorKey, err := s.parser.Profile(mediaType)
		if errors.Is(err, ErrServiceUnavailable) || errors.Is(err, ErrUnsupportedFileType) {
			continue
		}
		if err != nil {
			return fmt.Errorf("profile current library parser for %s: %w", mediaType, err)
		}
		availableMediaTypes = append(availableMediaTypes, mediaType)
		processorKeys = append(processorKeys, processorKey)
		profiles[mediaType] = processorKey
	}
	if len(availableMediaTypes) == 0 {
		return nil
	}
	staleBefore := time.Now().UTC().Add(-s.staleDerivationAfter)
	rows, err := s.q.ListStaleLibraryDerivation(ctx, sqlc.ListStaleLibraryDerivationParams{
		MediaTypes:    availableMediaTypes,
		ProcessorKeys: processorKeys,
		StaleBefore:   staleBefore,
		Limit:         reconciliationBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list stale library derivations: %w", err)
	}
	type staleFile struct {
		needsCurrentWork bool
		desiredStatus    ChunkSetStatus
	}
	files := make(map[string]*staleFile, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		state, ok := files[row.ID]
		if !ok {
			state = &staleFile{}
			files[row.ID] = state
			order = append(order, row.ID)
		}
		processorKey := profiles[row.MediaType]
		currentKey, err := libraryDerivationKey(row.RawSha256, row.MediaType, processorKey)
		if err != nil {
			return fmt.Errorf("derive current library generation for %s: %w", row.ID, err)
		}
		if row.ChunkSetID.Valid {
			currentSet := row.ChunkSetDerivationKey.Valid &&
				row.ChunkSetProcessorKey.Valid &&
				row.ChunkSetDerivationKey.String == currentKey &&
				row.ChunkSetProcessorKey.String == processorKey
			if currentSet {
				state.needsCurrentWork = true
			} else {
				if err := s.retireSupersededBuildingSet(ctx, row.ID, row.ChunkSetID.String); err != nil {
					return fmt.Errorf("retire superseded library generation %s: %w", row.ChunkSetID.String, err)
				}
			}
		}
		desiredCurrent := row.DesiredChunkSetDerivationKey.Valid &&
			row.DesiredChunkSetProcessorKey.Valid &&
			row.DesiredChunkSetStatus.Valid &&
			row.DesiredChunkSetDerivationKey.String == currentKey &&
			row.DesiredChunkSetProcessorKey.String == processorKey
		if desiredCurrent {
			state.desiredStatus = ChunkSetStatus(row.DesiredChunkSetStatus.String)
		}
		if FileStatus(row.Status) == FileStatusProcessing {
			state.needsCurrentWork = true
		}
		activeCurrent := row.ActiveChunkSetDerivationKey.Valid &&
			row.ActiveChunkSetProcessorKey.Valid &&
			row.ActiveChunkSetDerivationKey.String == currentKey &&
			row.ActiveChunkSetProcessorKey.String == processorKey
		if FileStatus(row.Status) == FileStatusReady && !activeCurrent {
			state.needsCurrentWork = true
		}
	}
	for _, fileID := range order {
		state := files[fileID]
		if !state.needsCurrentWork || state.desiredStatus == ChunkSetStatusFailed {
			continue
		}
		job, found, err := latestLibraryJob(ctx, client, chunkArgs{}.Kind(), fileID)
		if err != nil {
			return fmt.Errorf("load library chunk job for %s: %w", fileID, err)
		}
		if found && slices.Contains(activeLibraryJobStates, job.State) {
			if job.State != rivertype.JobStateRunning {
				if affected, err := s.q.TouchLibraryFileDerivation(ctx, fileID); err != nil {
					return fmt.Errorf("rotate active library derivation %s: %w", fileID, err)
				} else if affected != 1 {
					return fmt.Errorf("rotate active library derivation %s: file changed", fileID)
				}
				continue
			}
			if job.AttemptedAt == nil || !job.AttemptedAt.Before(staleBefore) {
				continue
			}
			// The query only returns files whose derivation heartbeat is stale. The
			// in-process parser and every database statement are bounded, so this is
			// a crashed or wedged worker rather than healthy work.
			if err := finalizeStaleRunningLibraryJob(ctx, client, job); err != nil {
				return fmt.Errorf("cancel stale library derivation %s: %w", fileID, err)
			}
			// The stale row no longer occupies River uniqueness, so the idempotent
			// replacement below can take over without changing the global rescuer.
		}
		if found && job.State == rivertype.JobStateDiscarded && state.desiredStatus == ChunkSetStatusBuilding {
			if err := s.failStaleGeneration(ctx, fileID); err != nil {
				return err
			}
			continue
		}
		args := chunkArgs{FileID: fileID}
		options := args.InsertOpts()
		if _, err := client.Insert(ctx, args, &options); err != nil {
			return fmt.Errorf("re-enqueue stale library derivation %s: %w", fileID, err)
		}
		if affected, err := s.q.TouchLibraryFileDerivation(ctx, fileID); err != nil {
			return fmt.Errorf("touch re-enqueued library derivation %s: %w", fileID, err)
		} else if affected != 1 {
			return fmt.Errorf("touch re-enqueued library derivation %s: file changed", fileID)
		}
	}
	return nil
}

func (s *Service) retireSupersededBuildingSet(ctx context.Context, fileID, chunkSetID string) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin superseded library generation retirement: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockLibraryFileLifecycle(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock library file for superseded generation: %w", err)
	}
	set, err := queries.LockLibraryChunkSetLifecycle(ctx, chunkSetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock superseded LibraryChunkSet: %w", err)
	}
	if set.FileID != file.ID {
		return fmt.Errorf("superseded LibraryChunkSet %s belongs to another file", set.ID)
	}
	processorKey, err := s.parser.Profile(file.MediaType)
	if err != nil {
		return fmt.Errorf("profile current library parser: %w", err)
	}
	currentKey, err := libraryDerivationKey(file.RawSha256, file.MediaType, processorKey)
	if err != nil {
		return err
	}
	if set.DerivationKey == currentKey && set.ProcessorKey == processorKey {
		return nil
	}
	if ChunkSetStatus(set.Status) != ChunkSetStatusBuilding {
		return nil
	}
	// File then ChunkSet locking matches publication. Remove only unpublished
	// rows while the set is still building, then make the retirement terminal.
	if _, err := queries.DeleteBuildingLibraryChunks(ctx, set.ID); err != nil {
		return fmt.Errorf("delete superseded library chunks: %w", err)
	}
	if affected, err := queries.MarkLibraryChunkSetFailed(ctx, sqlc.MarkLibraryChunkSetFailedParams{
		ErrorMessage: nullableText("Superseded by a newer parser profile."), ID: set.ID,
	}); err != nil {
		return fmt.Errorf("mark superseded LibraryChunkSet failed: %w", err)
	} else if affected != 1 {
		return ErrGenerationChanged
	}
	return commitLibraryTransaction(ctx, tx)
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
	processorKey, err := s.parser.Profile(file.MediaType)
	if err != nil {
		return fmt.Errorf("profile current library parser: %w", err)
	}
	derivationKey, err := libraryDerivationKey(file.RawSha256, file.MediaType, processorKey)
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
	if !s.rawStore.SupportsOrphanCollection() {
		// A deployment identity is not part of the S3 key today. Skip unknown-key
		// deletion there so deployments sharing one bucket cannot delete each
		// other's immutable snapshots.
		return nil
	}
	cursor, err := s.loadOrphanCursor(ctx)
	if err != nil {
		return err
	}
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
			return s.storeOrphanCursor(ctx, "")
		}
		cursor = page.NextCursor
	}
	return s.storeOrphanCursor(ctx, cursor)
}

func (s *Service) loadOrphanCursor(ctx context.Context) (string, error) {
	setting, err := s.q.GetSetting(ctx, orphanCursorSettingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load library orphan cursor: %w", err)
	}
	return setting.Value, nil
}

func (s *Service) storeOrphanCursor(ctx context.Context, cursor string) error {
	if err := s.q.UpsertSetting(ctx, sqlc.UpsertSettingParams{
		Key: orphanCursorSettingKey, Value: cursor,
	}); err != nil {
		return fmt.Errorf("store library orphan cursor: %w", err)
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
