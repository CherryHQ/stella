package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	KnowledgeQueue = "stella_knowledge"

	defaultReconciliationInterval = 5 * time.Minute
	defaultStaleAfter             = 15 * time.Minute
	reconciliationBatchSize       = 100
)

var activeKnowledgeJobStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
	rivertype.JobStateRetryable,
}

type parseArgs struct {
	FileID string `json:"file_id" river:"unique"`
}

func (parseArgs) Kind() string { return "stella_knowledge_parse" }

func (parseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       KnowledgeQueue,
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: append([]rivertype.JobState(nil), activeKnowledgeJobStates...),
		},
	}
}

type parseWorker struct {
	river.WorkerDefaults[parseArgs]
	service *Service
	logger  *slog.Logger
}

func (w *parseWorker) Work(ctx context.Context, job *river.Job[parseArgs]) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("knowledge parser panicked: %v", value)
		}
	}()
	return w.service.processParseJob(ctx, job)
}

func (s *Service) processParseJob(ctx context.Context, job *river.Job[parseArgs]) error {
	row, err := s.q.GetKnowledgeFileForParse(ctx, job.Args.FileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load knowledge file for parse: %w", err)
	}
	if FileStatus(row.Status) != FileStatusProcessing {
		return nil
	}
	if _, err := s.q.TouchProcessingKnowledgeFile(ctx, row.ID); err != nil {
		return fmt.Errorf("touch processing knowledge file: %w", err)
	}

	// Delete may race with the initial raw-content read. Re-check immediately
	// before starting the external parser; a concurrent delete also cancels the
	// River job context so an already-running Xberg process is interrupted.
	current, err := s.q.GetKnowledgeFile(ctx, row.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recheck knowledge file before parse: %w", err)
	}
	if FileStatus(current.Status) != FileStatusProcessing {
		return nil
	}

	chunks, parseErr := s.parseRawContent(ctx, row.ID, row.MediaType, row.RawContent)
	if parseErr != nil {
		if isDeterministicParseError(parseErr) || job.Attempt >= job.MaxAttempts {
			return s.completeFailedJob(ctx, job, publicParseError(parseErr))
		}
		return parseErr
	}
	return s.completeReadyJob(ctx, job, chunks)
}

func (s *Service) parseRawContent(
	ctx context.Context,
	fileID string,
	mediaType string,
	content []byte,
) ([]ParsedChunk, error) {
	tempDir, err := os.MkdirTemp(s.tempDir, "stella-knowledge-*")
	if err != nil {
		return nil, fmt.Errorf("create knowledge parse directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			s.logger.Warn("failed to remove knowledge parse directory", "file_id", fileID, "error", removeErr)
		}
	}()

	path := filepath.Join(tempDir, "source"+extensionForMediaType(mediaType))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("write knowledge parse input: %w", err)
	}
	chunks, err := s.parser.Parse(ctx, path, mediaType)
	if err != nil {
		return nil, err
	}
	return chunks, nil
}

func (s *Service) completeReadyJob(
	ctx context.Context,
	job *river.Job[parseArgs],
	chunks []ParsedChunk,
) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge parse completion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := s.q.WithTx(tx)

	state, err := qtx.GetKnowledgeFileStateForUpdate(ctx, job.Args.FileID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return fmt.Errorf("complete deleted knowledge job: %w", err)
		}
		return commitKnowledgeTransaction(ctx, tx)
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file for completion: %w", err)
	}
	if FileStatus(state.Status) != FileStatusProcessing {
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return fmt.Errorf("complete terminal knowledge job: %w", err)
		}
		return commitKnowledgeTransaction(ctx, tx)
	}

	if err := qtx.DeleteKnowledgeChunks(ctx, job.Args.FileID); err != nil {
		return fmt.Errorf("clear knowledge chunks: %w", err)
	}
	ordinals := make([]int64, 0, len(chunks))
	contents := make([]string, 0, len(chunks))
	locators := make([]string, 0, len(chunks))
	for ordinal, chunk := range chunks {
		locator, err := json.Marshal(chunk.Locator)
		if err != nil {
			return fmt.Errorf("encode knowledge chunk locator: %w", err)
		}
		ordinals = append(ordinals, int64(ordinal))
		contents = append(contents, chunk.Content)
		locators = append(locators, string(locator))
	}
	// Stella deliberately registers UUID as text-only in pgx, so COPY's
	// binary-only protocol is not safe for this table. One typed UNNEST query
	// preserves a single round trip while keeping the UUID wire contract intact.
	inserted, err := qtx.InsertKnowledgeChunks(ctx, sqlc.InsertKnowledgeChunksParams{
		FileID:   job.Args.FileID,
		Ordinals: ordinals,
		Contents: contents,
		Locators: locators,
	})
	if err != nil {
		return fmt.Errorf("insert knowledge chunks: %w", err)
	}
	if inserted != int64(len(chunks)) {
		return fmt.Errorf("insert knowledge chunks: wrote %d of %d rows", inserted, len(chunks))
	}
	updated, err := qtx.MarkKnowledgeFileReady(ctx, job.Args.FileID)
	if err != nil {
		return fmt.Errorf("mark knowledge file ready: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("mark knowledge file ready: unexpected rows affected %d", updated)
	}
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
		return fmt.Errorf("complete knowledge parse job: %w", err)
	}
	return commitKnowledgeTransaction(ctx, tx)
}

func (s *Service) completeFailedJob(
	ctx context.Context,
	job *river.Job[parseArgs],
	message string,
) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge failure completion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := s.q.WithTx(tx)

	state, err := qtx.GetKnowledgeFileStateForUpdate(ctx, job.Args.FileID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return fmt.Errorf("complete deleted knowledge job: %w", err)
		}
		return commitKnowledgeTransaction(ctx, tx)
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file for failure: %w", err)
	}
	if FileStatus(state.Status) == FileStatusProcessing {
		if err := qtx.DeleteKnowledgeChunks(ctx, job.Args.FileID); err != nil {
			return fmt.Errorf("clear failed knowledge chunks: %w", err)
		}
		updated, err := qtx.MarkKnowledgeFileFailed(ctx, sqlc.MarkKnowledgeFileFailedParams{
			ID:           job.Args.FileID,
			ErrorMessage: nullableText(cleanErrorMessage(message)),
		})
		if err != nil {
			return fmt.Errorf("mark knowledge file failed: %w", err)
		}
		if updated != 1 {
			return fmt.Errorf("mark knowledge file failed: unexpected rows affected %d", updated)
		}
	}
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
		return fmt.Errorf("complete failed knowledge parse job: %w", err)
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
		errors.Is(err, ErrParseResultLimit)
}

func publicParseError(err error) string {
	switch {
	case errors.Is(err, ErrNoExtractedText):
		return "No extractable text was found in this document."
	case errors.Is(err, ErrParseOutputLimit), errors.Is(err, ErrParseResultLimit):
		return "The parsed document exceeds the supported limits."
	case errors.Is(err, ErrInvalidXbergJSON):
		return "The document parser returned an invalid result."
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

type reconcileArgs struct{}

func (reconcileArgs) Kind() string { return "stella_knowledge_reconcile" }

func (reconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: KnowledgeQueue,
		UniqueOpts: river.UniqueOpts{
			ByState: append([]rivertype.JobState(nil), activeKnowledgeJobStates...),
		},
	}
}

type reconcileWorker struct {
	river.WorkerDefaults[reconcileArgs]
	service *Service
	logger  *slog.Logger
}

func (w *reconcileWorker) Work(ctx context.Context, _ *river.Job[reconcileArgs]) error {
	if err := w.service.reconcile(ctx); err != nil {
		w.logger.Warn("knowledge reconciliation failed", "error", err)
		return err
	}
	return nil
}

func (s *Service) reconcile(ctx context.Context) error {
	if s.river == nil {
		return ErrServiceUnavailable
	}
	rows, err := s.q.ListStaleProcessingKnowledgeFiles(ctx, sqlc.ListStaleProcessingKnowledgeFilesParams{
		StaleBefore: time.Now().UTC().Add(-s.staleAfter),
		Limit:       reconciliationBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list stale knowledge files: %w", err)
	}
	for _, row := range rows {
		state, found, err := s.latestParseJobState(ctx, row.ID)
		if err != nil {
			return err
		}
		switch {
		case found && isActiveKnowledgeJobState(state):
			continue
		case found && state == rivertype.JobStateDiscarded:
			if err := s.failProcessingFile(ctx, row.ID, "Document parsing failed after multiple attempts."); err != nil {
				return err
			}
		default:
			if _, err := s.river.Insert(ctx, parseArgs{FileID: row.ID}, nil); err != nil {
				return fmt.Errorf("re-enqueue stale knowledge file %s: %w", row.ID, err)
			}
		}
	}
	return nil
}

func (s *Service) latestParseJobState(
	ctx context.Context,
	fileID string,
) (rivertype.JobState, bool, error) {
	result, err := s.river.JobList(
		ctx,
		river.NewJobListParams().
			Kinds(parseArgs{}.Kind()).
			States(rivertype.JobStates()...).
			Where("args ->> 'file_id' = @file_id", river.NamedArgs{"file_id": fileID}).
			OrderBy(river.JobListOrderByID, river.SortOrderDesc).
			First(1),
	)
	if err != nil {
		return "", false, fmt.Errorf("list River job for knowledge file %s: %w", fileID, err)
	}
	if len(result.Jobs) == 0 {
		return "", false, nil
	}
	return result.Jobs[0].State, true, nil
}

func (s *Service) activeParseJobIDs(ctx context.Context, fileID string) ([]int64, error) {
	result, err := s.river.JobList(
		ctx,
		river.NewJobListParams().
			Kinds(parseArgs{}.Kind()).
			States(activeKnowledgeJobStates...).
			Where("args ->> 'file_id' = @file_id", river.NamedArgs{"file_id": fileID}).
			OrderBy(river.JobListOrderByID, river.SortOrderDesc).
			First(100),
	)
	if err != nil {
		return nil, fmt.Errorf("list active River jobs for knowledge file %s: %w", fileID, err)
	}
	jobIDs := make([]int64, 0, len(result.Jobs))
	for _, job := range result.Jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	return jobIDs, nil
}

func isActiveKnowledgeJobState(state rivertype.JobState) bool {
	return slices.Contains(activeKnowledgeJobStates, state)
}

func (s *Service) failProcessingFile(ctx context.Context, fileID, message string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin stale knowledge failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := s.q.WithTx(tx)
	state, err := qtx.GetKnowledgeFileStateForUpdate(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock stale knowledge file: %w", err)
	}
	if FileStatus(state.Status) != FileStatusProcessing {
		return nil
	}
	if err := qtx.DeleteKnowledgeChunks(ctx, fileID); err != nil {
		return fmt.Errorf("clear stale knowledge chunks: %w", err)
	}
	if _, err := qtx.MarkKnowledgeFileFailed(ctx, sqlc.MarkKnowledgeFileFailedParams{
		ID:           fileID,
		ErrorMessage: nullableText(cleanErrorMessage(message)),
	}); err != nil {
		return fmt.Errorf("mark stale knowledge file failed: %w", err)
	}
	return commitKnowledgeTransaction(ctx, tx)
}

// QueueConfig returns the dedicated per-node worker limit.
func (s *Service) QueueConfig() (string, river.QueueConfig) {
	return KnowledgeQueue, river.QueueConfig{MaxWorkers: 2}
}

// RegisterRiverWorkers contributes parsing and reconciliation workers to the
// process-wide River client.
func (s *Service) RegisterRiverWorkers(workers *river.Workers) {
	river.AddWorker(workers, &parseWorker{service: s, logger: s.logger.With("worker", "parse")})
	river.AddWorker(workers, &reconcileWorker{service: s, logger: s.logger.With("worker", "reconcile")})
}

// StartReconciliation registers the single-leader periodic repair pass.
func (s *Service) StartReconciliation() (rivertype.PeriodicJobHandle, error) {
	if s.river == nil {
		return 0, ErrServiceUnavailable
	}
	handle := s.river.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(s.reconciliationInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			opts := reconcileArgs{}.InsertOpts()
			return reconcileArgs{}, &opts
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))
	return handle, nil
}

// StopReconciliation removes the periodic source; in-flight work drains with
// the shared River client.
func (s *Service) StopReconciliation(handle rivertype.PeriodicJobHandle) {
	if s.river != nil {
		s.river.PeriodicJobs().Remove(handle)
	}
}
