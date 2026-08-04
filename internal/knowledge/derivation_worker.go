package knowledge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type generationAction uint8

const (
	generationBuild generationAction = iota
	generationPublishExisting
	generationComplete
)

type generationTarget struct {
	FileID        string
	MediaType     string
	SizeBytes     int64
	RawSHA256     []byte
	ChunkSetID    string
	DerivationKey string
}

func (s *Service) processChunkJob(ctx context.Context, job *river.Job[chunkArgs]) error {
	target, action, err := s.prepareChunkGeneration(ctx, job.Args.FileID)
	if err != nil {
		return err
	}
	switch action {
	case generationComplete:
		return s.completeChunkJob(ctx, job)
	case generationPublishExisting:
		return s.publishExistingChunkSet(ctx, job, target)
	}

	parsed, parseErr := s.parseRawSnapshot(ctx, target)
	if parseErr != nil {
		if errors.Is(parseErr, errGenerationStopped) {
			return s.completeChunkJob(ctx, job)
		}
		if isDeterministicParseError(parseErr) || job.Attempt >= job.MaxAttempts {
			return s.failChunkJob(ctx, job, target, publicParseError(parseErr))
		}
		return parseErr
	}
	chunks, digest, err := normalizeParsedChunks(parsed)
	if err != nil {
		return s.failChunkJob(ctx, job, target, publicParseError(err))
	}
	for _, batch := range chunkBatches(chunks) {
		if err := s.stageChunkBatch(ctx, target, batch); err != nil {
			switch {
			case errors.Is(err, errGenerationStopped):
				return s.completeChunkJob(ctx, job)
			case errors.Is(err, ErrGenerationChanged):
				return s.finishChangedGeneration(ctx, job, target)
			case errors.Is(err, ErrGenerationConflict):
				return s.failChunkJob(ctx, job, target, publicParseError(err))
			default:
				return err
			}
		}
	}
	if err := s.publishChunkSet(ctx, job, target, int64(len(chunks)), digest); err != nil {
		if errors.Is(err, errGenerationStopped) {
			return s.completeChunkJob(ctx, job)
		}
		if errors.Is(err, ErrGenerationConflict) {
			return s.failChunkJob(ctx, job, target, publicParseError(err))
		}
		return err
	}
	return nil
}

var errGenerationStopped = errors.New("knowledge generation stopped")

func (s *Service) prepareChunkGeneration(
	ctx context.Context,
	fileID string,
) (generationTarget, generationAction, error) {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return generationTarget{}, generationComplete, fmt.Errorf("begin knowledge generation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	file, err := queries.LockKnowledgeFileLifecycle(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return generationTarget{}, generationComplete, nil
	}
	if err != nil {
		return generationTarget{}, generationComplete, fmt.Errorf("lock knowledge file for generation: %w", err)
	}
	if file.DeletedAt.Valid || FileStatus(file.Status) == FileStatusFailed {
		return generationTarget{}, generationComplete, nil
	}
	if FileStatus(file.Status) != FileStatusProcessing && FileStatus(file.Status) != FileStatusReady {
		return generationTarget{}, generationComplete, fmt.Errorf("%w: unsupported file status %q", ErrGenerationChanged, file.Status)
	}
	derivationKey, err := knowledgeDerivationKey(file.RawSha256, file.MediaType)
	if err != nil {
		return generationTarget{}, generationComplete, err
	}
	target := generationTarget{
		FileID:        file.ID,
		MediaType:     file.MediaType,
		SizeBytes:     file.SizeBytes,
		RawSHA256:     append([]byte(nil), file.RawSha256...),
		DerivationKey: derivationKey,
	}

	setID, err := uuid.NewV7()
	if err != nil {
		return generationTarget{}, generationComplete, fmt.Errorf("generate knowledge ChunkSet ID: %w", err)
	}
	if _, err := queries.CreateKnowledgeChunkSet(ctx, sqlc.CreateKnowledgeChunkSetParams{
		ID:            setID.String(),
		FileID:        file.ID,
		DerivationKey: derivationKey,
		ProcessorKey:  XbergProcessorKey,
		RawSha256:     append([]byte(nil), file.RawSha256...),
	}); err != nil {
		return generationTarget{}, generationComplete, fmt.Errorf("create knowledge ChunkSet: %w", err)
	}
	set, err := queries.GetKnowledgeChunkSetByDerivation(ctx, sqlc.GetKnowledgeChunkSetByDerivationParams{
		FileID: file.ID, DerivationKey: derivationKey,
	})
	if err != nil {
		return generationTarget{}, generationComplete, fmt.Errorf("load knowledge ChunkSet: %w", err)
	}
	if err := validateGenerationIdentity(set, target); err != nil {
		return generationTarget{}, generationComplete, err
	}
	target.ChunkSetID = set.ID
	if err := commitKnowledgeTransaction(ctx, tx); err != nil {
		return generationTarget{}, generationComplete, err
	}

	switch ChunkSetStatus(set.Status) {
	case ChunkSetStatusBuilding:
		return target, generationBuild, nil
	case ChunkSetStatusReady:
		return target, generationPublishExisting, nil
	case ChunkSetStatusFailed:
		return target, generationComplete, nil
	default:
		return generationTarget{}, generationComplete, fmt.Errorf("%w: unsupported ChunkSet status %q", ErrGenerationChanged, set.Status)
	}
}

func validateGenerationIdentity(set sqlc.KnowledgeChunkSet, target generationTarget) error {
	if set.FileID != target.FileID || set.DerivationKey != target.DerivationKey || set.ProcessorKey != XbergProcessorKey {
		return fmt.Errorf("%w: ChunkSet identity mismatch", ErrGenerationConflict)
	}
	if subtle.ConstantTimeCompare(set.RawSha256, target.RawSHA256) != 1 {
		return fmt.Errorf("%w: ChunkSet raw hash mismatch", ErrGenerationConflict)
	}
	return nil
}

func (s *Service) parseRawSnapshot(ctx context.Context, target generationTarget) ([]ParsedChunk, error) {
	if err := s.ensureGenerationLive(ctx, target); err != nil {
		return nil, err
	}
	rawKey, err := RawKey(target.FileID)
	if err != nil {
		return nil, err
	}
	raw, err := s.rawStore.Open(ctx, rawKey)
	if err != nil {
		return nil, fmt.Errorf("open knowledge raw snapshot: %w", err)
	}
	defer func() { _ = raw.Close() }()

	directory, err := os.MkdirTemp(s.tempDir, "stella-knowledge-parse-*")
	if err != nil {
		return nil, fmt.Errorf("create knowledge parse directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(directory); removeErr != nil {
			s.logger.Warn("remove knowledge parse directory", "file_id", target.FileID, "error", removeErr)
		}
	}()
	path := filepath.Join(directory, "source"+extensionForMediaType(target.MediaType))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create knowledge parse input: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.CopyBuffer(
		io.MultiWriter(file, hash),
		io.LimitReader(contextReader(ctx, raw), MaxFileBytes+1),
		make([]byte, spoolCopyBufferSize),
	)
	closeErr := file.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("stage knowledge raw snapshot: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close knowledge parse input: %w", closeErr)
	}
	if written != target.SizeBytes || written > MaxFileBytes || subtle.ConstantTimeCompare(hash.Sum(nil), target.RawSHA256) != 1 {
		return nil, fmt.Errorf("%w: size or SHA-256 mismatch", ErrRawIntegrity)
	}
	if err := s.ensureGenerationLive(ctx, target); err != nil {
		return nil, err
	}
	return s.parser.Parse(ctx, path, target.MediaType)
}

func (s *Service) ensureGenerationLive(ctx context.Context, target generationTarget) error {
	file, err := s.q.GetKnowledgeFile(ctx, target.FileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errGenerationStopped
	}
	if err != nil {
		return fmt.Errorf("reload knowledge file generation: %w", err)
	}
	if subtle.ConstantTimeCompare(file.RawSha256, target.RawSHA256) != 1 {
		return ErrGenerationChanged
	}
	set, err := s.q.GetKnowledgeChunkSetByDerivation(ctx, sqlc.GetKnowledgeChunkSetByDerivationParams{
		FileID: target.FileID, DerivationKey: target.DerivationKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGenerationChanged
	}
	if err != nil {
		return fmt.Errorf("reload knowledge ChunkSet: %w", err)
	}
	if err := validateGenerationIdentity(set, target); err != nil {
		return err
	}
	if ChunkSetStatus(set.Status) != ChunkSetStatusBuilding {
		return ErrGenerationChanged
	}
	return nil
}

func (s *Service) stageChunkBatch(
	ctx context.Context,
	target generationTarget,
	batch []stagedChunk,
) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge chunk staging: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockKnowledgeFileLifecycle(ctx, target.FileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		return errGenerationStopped
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file for staging: %w", err)
	}
	set, err := queries.LockKnowledgeChunkSetLifecycle(ctx, target.ChunkSetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGenerationChanged
	}
	if err != nil {
		return fmt.Errorf("lock KnowledgeChunkSet for staging: %w", err)
	}
	if err := validateGenerationIdentity(set, target); err != nil {
		return err
	}
	if ChunkSetStatus(set.Status) != ChunkSetStatusBuilding {
		return ErrGenerationChanged
	}

	params := sqlc.InsertKnowledgeChunkBatchParams{ChunkSetID: target.ChunkSetID}
	params.Ids = make([]string, 0, len(batch))
	params.Ordinals = make([]int64, 0, len(batch))
	params.Contents = make([]string, 0, len(batch))
	params.Locators = make([]string, 0, len(batch))
	params.ContentSha256s = make([][]byte, 0, len(batch))
	for _, chunk := range batch {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate knowledge chunk ID: %w", err)
		}
		params.Ids = append(params.Ids, id.String())
		params.Ordinals = append(params.Ordinals, chunk.Ordinal)
		params.Contents = append(params.Contents, chunk.Content)
		params.Locators = append(params.Locators, chunk.LocatorJSON)
		params.ContentSha256s = append(params.ContentSha256s, append([]byte(nil), chunk.ContentSHA256[:]...))
	}
	if _, err := queries.InsertKnowledgeChunkBatch(ctx, params); err != nil {
		return fmt.Errorf("stage knowledge chunk batch: %w", err)
	}
	rows, err := queries.ListKnowledgeChunkByOrdinals(ctx, sqlc.ListKnowledgeChunkByOrdinalsParams{
		ChunkSetID: target.ChunkSetID, Ordinals: params.Ordinals,
	})
	if err != nil {
		return fmt.Errorf("verify knowledge chunk batch: %w", err)
	}
	if err := verifyStagedBatch(batch, rows); err != nil {
		return err
	}
	if affected, err := queries.TouchKnowledgeFileDerivation(ctx, target.FileID); err != nil {
		return fmt.Errorf("touch knowledge derivation: %w", err)
	} else if affected != 1 {
		return errGenerationStopped
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func verifyStagedBatch(batch []stagedChunk, rows []sqlc.ListKnowledgeChunkByOrdinalsRow) error {
	if len(rows) != len(batch) {
		return fmt.Errorf("%w: staged row count %d, want %d", ErrGenerationConflict, len(rows), len(batch))
	}
	for index, want := range batch {
		got := rows[index]
		if got.Ordinal != want.Ordinal || got.Content != want.Content || subtle.ConstantTimeCompare(got.ContentSha256, want.ContentSHA256[:]) != 1 {
			return fmt.Errorf("%w: ordinal %d content differs", ErrGenerationConflict, want.Ordinal)
		}
		var gotLocator, wantLocator any
		if err := json.Unmarshal(got.Locator, &gotLocator); err != nil {
			return fmt.Errorf("%w: stored locator is invalid", ErrGenerationConflict)
		}
		if err := json.Unmarshal([]byte(want.LocatorJSON), &wantLocator); err != nil {
			return fmt.Errorf("encode expected chunk locator: %w", err)
		}
		if !reflect.DeepEqual(gotLocator, wantLocator) {
			return fmt.Errorf("%w: ordinal %d locator differs", ErrGenerationConflict, want.Ordinal)
		}
	}
	return nil
}

func (s *Service) publishChunkSet(
	ctx context.Context,
	job *river.Job[chunkArgs],
	target generationTarget,
	chunkCount int64,
	expectedDigest []byte,
) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge ChunkSet publication: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockKnowledgeFileLifecycle(ctx, target.FileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		if err := completeRiverJobTx(ctx, tx, job); err != nil {
			return err
		}
		return commitKnowledgeTransaction(ctx, tx)
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file for publication: %w", err)
	}
	set, err := queries.LockKnowledgeChunkSetLifecycle(ctx, target.ChunkSetID)
	if err != nil {
		return fmt.Errorf("lock KnowledgeChunkSet for publication: %w", err)
	}
	if err := validateGenerationIdentity(set, target); err != nil {
		return err
	}
	if ChunkSetStatus(set.Status) == ChunkSetStatusReady {
		return s.publishLockedReadySet(ctx, tx, queries, job, file, set)
	}
	if ChunkSetStatus(set.Status) != ChunkSetStatusBuilding {
		return ErrGenerationChanged
	}
	integrity, err := queries.GetKnowledgeChunkSetIntegrity(ctx, target.ChunkSetID)
	if err != nil {
		return fmt.Errorf("validate knowledge ChunkSet integrity: %w", err)
	}
	if integrity.ChunkCount != chunkCount || integrity.MinOrdinal != 0 || integrity.MaxOrdinal != chunkCount-1 || subtle.ConstantTimeCompare(integrity.ContentDigest, expectedDigest) != 1 {
		return fmt.Errorf("%w: staged generation integrity mismatch", ErrGenerationConflict)
	}
	if affected, err := queries.MarkKnowledgeChunkSetReady(ctx, sqlc.MarkKnowledgeChunkSetReadyParams{
		ChunkCount:    pgtype.Int8{Int64: chunkCount, Valid: true},
		ContentDigest: append([]byte(nil), expectedDigest...),
		ID:            target.ChunkSetID,
	}); err != nil {
		return fmt.Errorf("mark knowledge ChunkSet ready: %w", err)
	} else if affected != 1 {
		return ErrGenerationChanged
	}
	set.Status = string(ChunkSetStatusReady)
	return s.publishLockedReadySet(ctx, tx, queries, job, file, set)
}

func (s *Service) publishExistingChunkSet(
	ctx context.Context,
	job *river.Job[chunkArgs],
	target generationTarget,
) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin existing ChunkSet publication: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockKnowledgeFileLifecycle(ctx, target.FileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		if err := completeRiverJobTx(ctx, tx, job); err != nil {
			return err
		}
		return commitKnowledgeTransaction(ctx, tx)
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file for existing publication: %w", err)
	}
	set, err := queries.LockKnowledgeChunkSetLifecycle(ctx, target.ChunkSetID)
	if err != nil {
		return fmt.Errorf("lock existing KnowledgeChunkSet: %w", err)
	}
	if err := validateGenerationIdentity(set, target); err != nil {
		return err
	}
	if ChunkSetStatus(set.Status) != ChunkSetStatusReady {
		return ErrGenerationChanged
	}
	return s.publishLockedReadySet(ctx, tx, queries, job, file, set)
}

func (s *Service) publishLockedReadySet(
	ctx context.Context,
	tx pgx.Tx,
	queries *sqlc.Queries,
	job *river.Job[chunkArgs],
	file sqlc.LockKnowledgeFileLifecycleRow,
	set sqlc.KnowledgeChunkSet,
) error {
	if file.DeletedAt.Valid {
		return errGenerationStopped
	}
	if affected, err := queries.PublishKnowledgeFileChunkSet(ctx, sqlc.PublishKnowledgeFileChunkSetParams{
		ChunkSetID: pgtype.Text{String: set.ID, Valid: true}, ID: file.ID,
	}); err != nil {
		return fmt.Errorf("publish knowledge ChunkSet: %w", err)
	} else if affected != 1 {
		return errGenerationStopped
	}
	if err := completeRiverJobTx(ctx, tx, job); err != nil {
		return err
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func (s *Service) finishChangedGeneration(
	ctx context.Context,
	job *river.Job[chunkArgs],
	target generationTarget,
) error {
	set, err := s.q.GetKnowledgeChunkSetByDerivation(ctx, sqlc.GetKnowledgeChunkSetByDerivationParams{
		FileID: target.FileID, DerivationKey: target.DerivationKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGenerationChanged
	}
	if err != nil {
		return err
	}
	if ChunkSetStatus(set.Status) == ChunkSetStatusReady {
		return s.publishExistingChunkSet(ctx, job, target)
	}
	if ChunkSetStatus(set.Status) == ChunkSetStatusFailed {
		return s.completeChunkJob(ctx, job)
	}
	return ErrGenerationChanged
}

func (s *Service) failChunkJob(
	ctx context.Context,
	job *river.Job[chunkArgs],
	target generationTarget,
	message string,
) error {
	tx, queries, err := s.beginBoundedTx(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge generation failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	file, err := queries.LockKnowledgeFileLifecycle(ctx, target.FileID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && file.DeletedAt.Valid {
		if err := completeRiverJobTx(ctx, tx, job); err != nil {
			return err
		}
		return commitKnowledgeTransaction(ctx, tx)
	}
	if err != nil {
		return fmt.Errorf("lock failed knowledge file: %w", err)
	}
	set, err := queries.LockKnowledgeChunkSetLifecycle(ctx, target.ChunkSetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock failed KnowledgeChunkSet: %w", err)
	}
	if err == nil && ChunkSetStatus(set.Status) == ChunkSetStatusBuilding {
		if _, err := queries.MarkKnowledgeChunkSetFailed(ctx, sqlc.MarkKnowledgeChunkSetFailedParams{
			ErrorMessage: nullableText(cleanErrorMessage(message)), ID: set.ID,
		}); err != nil {
			return fmt.Errorf("mark KnowledgeChunkSet failed: %w", err)
		}
	}
	if _, err := queries.MarkKnowledgeFileFailedWithoutActiveSet(ctx, sqlc.MarkKnowledgeFileFailedWithoutActiveSetParams{
		ErrorMessage: nullableText(cleanErrorMessage(message)), ID: file.ID,
	}); err != nil {
		return fmt.Errorf("mark knowledge file failed: %w", err)
	}
	if err := completeRiverJobTx(ctx, tx, job); err != nil {
		return err
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func (s *Service) completeChunkJob(ctx context.Context, job *river.Job[chunkArgs]) error {
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

func commitKnowledgeTransaction(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge transaction: %w", err)
	}
	return nil
}

func isDeterministicParseError(err error) bool {
	return errors.Is(err, ErrNoExtractedText) ||
		errors.Is(err, ErrParseOutputLimit) ||
		errors.Is(err, ErrInvalidXbergJSON) ||
		errors.Is(err, ErrParseResultLimit) ||
		errors.Is(err, ErrRawIntegrity) ||
		errors.Is(err, ErrGenerationConflict)
}

func publicParseError(err error) string {
	switch {
	case errors.Is(err, ErrNoExtractedText):
		return "No extractable text was found in this document."
	case errors.Is(err, ErrParseOutputLimit), errors.Is(err, ErrParseResultLimit):
		return "The parsed document exceeds the supported limits."
	case errors.Is(err, ErrInvalidXbergJSON):
		return "The document parser returned an invalid result."
	case errors.Is(err, ErrRawIntegrity):
		return "The stored document failed integrity validation."
	case errors.Is(err, ErrGenerationConflict):
		return "The parsed document produced inconsistent chunks."
	default:
		return "Document parsing failed after multiple attempts."
	}
}

func cleanErrorMessage(message string) string {
	message = sanitizeXbergDiagnostic(message)
	if message == "" {
		return "Document parsing failed."
	}
	return message
}

func extensionForMediaType(mediaType string) string {
	switch mediaType {
	case MediaTypePDF:
		return ".pdf"
	case MediaTypeDOCX:
		return ".docx"
	case MediaTypeMarkdown:
		return ".md"
	default:
		return ".txt"
	}
}
