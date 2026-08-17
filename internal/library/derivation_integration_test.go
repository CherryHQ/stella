package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestChunkWorkerStagesAndAtomicallyPublishesGeneration(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed := []ParsedChunk{
		{Content: "# Travel policy", Locator: ChunkLocator{HeadingPath: []string{"Travel"}, ByteEnd: 15}},
		{Content: "Flights require approval.", Locator: ChunkLocator{ByteStart: 16, ByteEnd: 41}},
		{Content: "Hotels are capped at 800 yuan.", Locator: ChunkLocator{ByteStart: 42, ByteEnd: 72}},
	}
	service, client := newWorkingLibraryService(t, database, store, parserFunc(func(
		context.Context,
		string,
		string,
	) ([]ParsedChunk, error) {
		return parsed, nil
	}))

	file, err := service.CreateManagedUpload(
		t.Context(),
		testAuthority(t, testUserA, true),
		ScopeSystem,
		"",
		"travel.txt",
		bytes.NewBufferString("travel source"),
	)
	if err != nil {
		t.Fatalf("CreateManagedUpload: %v", err)
	}
	ready := waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady && current.ActiveChunkSetID != ""
	})

	set, err := sqlc.New(database).GetReadyLibraryChunkSetByDerivation(
		t.Context(),
		sqlc.GetReadyLibraryChunkSetByDerivationParams{
			FileID:        file.ID,
			DerivationKey: mustLibraryDerivationKey(t, file.RawSHA256, file.MediaType),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if set.ID != ready.ActiveChunkSetID || ChunkSetStatus(set.Status) != ChunkSetStatusReady {
		t.Fatalf("published ChunkSet = %+v; active pointer = %q", set, ready.ActiveChunkSetID)
	}
	if !set.UpdatedAt.After(set.CreatedAt) {
		t.Fatalf(
			"ready ChunkSet updated_at = %s, want after created_at %s",
			set.UpdatedAt,
			set.CreatedAt,
		)
	}
	staged, digest, err := normalizeParsedChunks(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if set.ChunkCount.Int64 != int64(len(staged)) || !bytes.Equal(set.ContentDigest, digest) {
		t.Fatalf("ChunkSet integrity = count %d digest %x; want %d %x", set.ChunkCount.Int64, set.ContentDigest, len(staged), digest)
	}
	rows, err := database.Query(t.Context(), `
		SELECT ordinal, content, locator, content_sha256
		FROM library_chunk
		WHERE chunk_set_id = $1
		ORDER BY ordinal ASC
	`, set.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for index := 0; rows.Next(); index++ {
		if index >= len(staged) {
			t.Fatal("persisted more chunks than the parser returned")
		}
		var ordinal int64
		var content string
		var locatorJSON []byte
		var contentHash []byte
		if err := rows.Scan(&ordinal, &content, &locatorJSON, &contentHash); err != nil {
			t.Fatal(err)
		}
		var gotLocator, wantLocator any
		if err := json.Unmarshal(locatorJSON, &gotLocator); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(staged[index].LocatorJSON), &wantLocator); err != nil {
			t.Fatal(err)
		}
		if ordinal != staged[index].Ordinal || content != staged[index].Content ||
			!bytes.Equal(contentHash, staged[index].ContentSHA256[:]) ||
			!reflect.DeepEqual(gotLocator, wantLocator) {
			t.Fatalf("persisted chunk %d differs from parser output", index)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestTransientPublicationIntegrityMismatchRetriesFromCleanGeneration(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the stale-worker race by dropping exactly one staged row from the
	// first attempt. The publication digest must reject that attempt without
	// making the file terminal; River then rebuilds the generation from scratch.
	if _, err := database.Exec(t.Context(), `
		CREATE TABLE library_test_chunk_skip_once (remaining integer NOT NULL);
		INSERT INTO library_test_chunk_skip_once (remaining) VALUES (1);
		CREATE FUNCTION library_test_skip_chunk_once() RETURNS trigger
		LANGUAGE plpgsql AS $function$
		DECLARE
			skipped boolean := false;
		BEGIN
			UPDATE library_test_chunk_skip_once
			SET remaining = remaining - 1
			WHERE remaining > 0
			RETURNING true INTO skipped;
			IF skipped THEN
				RETURN NULL;
			END IF;
			RETURN NEW;
		END;
		$function$;
		CREATE TRIGGER library_test_skip_chunk_once
		BEFORE INSERT ON library_chunk
		FOR EACH ROW EXECUTE FUNCTION library_test_skip_chunk_once();
	`); err != nil {
		t.Fatal(err)
	}
	service, client := newWorkingLibraryService(t, database, store, staticLibraryParser{})
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "retry.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady && current.ActiveChunkSetID != ""
	})
	job, found, err := latestLibraryJob(t.Context(), client, chunkArgs{}.Kind(), file.ID)
	if err != nil || !found {
		t.Fatalf("load retried chunk job: found=%t error=%v", found, err)
	}
	if job.State != rivertype.JobStateCompleted || job.Attempt < 2 {
		t.Fatalf("retried chunk job = state %q attempt %d, want completed after retry", job.State, job.Attempt)
	}
	var remaining, chunkCount int
	if err := database.QueryRow(t.Context(), `SELECT remaining FROM library_test_chunk_skip_once`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		t.Context(), `SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1`, ready.ActiveChunkSetID,
	).Scan(&chunkCount); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || chunkCount != 1 {
		t.Fatalf("recovered generation = remaining_skip %d chunks %d, want 0 and 1", remaining, chunkCount)
	}
}

func TestChunkRetryUsesAnIndependentAttemptSet(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "partial.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, action, err := service.prepareChunkGeneration(t.Context(), file.ID)
	if err != nil || action != generationBuild {
		t.Fatalf("prepareChunkGeneration = action %d, error %v", action, err)
	}
	chunks, _, err := normalizeParsedChunks([]ParsedChunk{
		{Content: "first chunk"},
		{Content: "second chunk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, action, err = service.createChunkAttempt(t.Context(), target)
	if err != nil || action != generationBuild {
		t.Fatalf("create first attempt = action %d, error %v", action, err)
	}
	if err := service.stageChunkBatch(t.Context(), target, chunks[:1]); err != nil {
		t.Fatal(err)
	}
	restarted := newLibraryServiceWithConfig(t, database, ServiceConfig{
		RawStore: store,
		Parser:   staticLibraryParser{},
	})
	restartedTarget, restartedAction, err := restarted.prepareChunkGeneration(t.Context(), file.ID)
	if err != nil || restartedAction != generationBuild {
		t.Fatalf("restart prepare = target %+v action %d error %v", restartedTarget, restartedAction, err)
	}
	restartedTarget, restartedAction, err = restarted.createChunkAttempt(t.Context(), restartedTarget)
	if err != nil || restartedAction != generationBuild {
		t.Fatalf("create restarted attempt = action %d, error %v", restartedAction, err)
	}
	if restartedTarget.ChunkSetID == target.ChunkSetID || restartedTarget.DerivationKey != target.DerivationKey {
		t.Fatalf("retry did not isolate the attempt: before=%+v after=%+v", target, restartedTarget)
	}
	var originalCount int
	if err := database.QueryRow(
		t.Context(), `SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1`, target.ChunkSetID,
	).Scan(&originalCount); err != nil {
		t.Fatal(err)
	}
	if originalCount != 1 {
		t.Fatalf("retry modified the first attempt; chunks = %d", originalCount)
	}
	if err := restarted.stageChunkBatch(t.Context(), restartedTarget, chunks); err != nil {
		t.Fatalf("clean rebuild failed: %v", err)
	}
	var count int
	var activeSetID *string
	if err := database.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1),
			active_chunk_set_id
		FROM library_file
		WHERE id = $2
	`, restartedTarget.ChunkSetID, file.ID).Scan(&count, &activeSetID); err != nil {
		t.Fatal(err)
	}
	if count != len(chunks) || activeSetID != nil {
		t.Fatalf("partial staging count = %d, active pointer = %v", count, activeSetID)
	}

	// Both insert-only River clients are deliberately not started. No background
	// worker can race this test's direct staging assertions.
}

func TestReconciliationRetiresAbandonedEquivalentAttempt(t *testing.T) {
	database := dbtest.New(t)
	_, service := newLibraryService(t, database)
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "attempts.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, action, err := service.prepareChunkGeneration(t.Context(), file.ID)
	if err != nil || action != generationBuild {
		t.Fatalf("prepare generation = action %d error %v", action, err)
	}
	parsed, digest, err := normalizeParsedChunks([]ParsedChunk{{Content: "published content"}})
	if err != nil {
		t.Fatal(err)
	}
	first, action, err := service.createChunkAttempt(t.Context(), target)
	if err != nil || action != generationBuild {
		t.Fatalf("create abandoned attempt = action %d error %v", action, err)
	}
	if err := service.stageChunkBatch(t.Context(), first, parsed); err != nil {
		t.Fatal(err)
	}
	second, action, err := service.createChunkAttempt(t.Context(), target)
	if err != nil || action != generationBuild {
		t.Fatalf("create winning attempt = action %d error %v", action, err)
	}
	if err := service.stageChunkBatch(t.Context(), second, parsed); err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(database)
	if affected, err := queries.MarkLibraryChunkSetReady(t.Context(), sqlc.MarkLibraryChunkSetReadyParams{
		ChunkCount:    pgtype.Int8{Int64: int64(len(parsed)), Valid: true},
		ContentDigest: digest,
		ID:            second.ChunkSetID,
	}); err != nil || affected != 1 {
		t.Fatalf("mark winning attempt ready = affected %d error %v", affected, err)
	}
	if affected, err := queries.PublishLibraryFileChunkSet(t.Context(), sqlc.PublishLibraryFileChunkSetParams{
		ChunkSetID: pgtype.Text{String: second.ChunkSetID, Valid: true}, ID: file.ID,
	}); err != nil || affected != 1 {
		t.Fatalf("publish winning attempt = affected %d error %v", affected, err)
	}
	client := service.riverClient()
	job, found, err := latestLibraryJob(t.Context(), client, chunkArgs{}.Kind(), file.ID)
	if err != nil || !found {
		t.Fatalf("load queued derivation job = found %t error %v", found, err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1
	`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE library_file SET updated_at = now() - interval '1 hour' WHERE id = $1
	`, file.ID); err != nil {
		t.Fatal(err)
	}
	service.staleDerivationAfter = time.Minute
	if err := service.reconcileStaleDerivations(t.Context()); err != nil {
		t.Fatal(err)
	}
	var abandonedStatus, winnerStatus, activeSetID string
	var abandonedChunks int
	if err := database.QueryRow(t.Context(), `
		SELECT
			(SELECT status FROM library_chunk_set WHERE id = $1),
			(SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1),
			(SELECT status FROM library_chunk_set WHERE id = $2),
			active_chunk_set_id
		FROM library_file WHERE id = $3
	`, first.ChunkSetID, second.ChunkSetID, file.ID).Scan(
		&abandonedStatus, &abandonedChunks, &winnerStatus, &activeSetID,
	); err != nil {
		t.Fatal(err)
	}
	if abandonedStatus != string(ChunkSetStatusFailed) || abandonedChunks != 0 ||
		winnerStatus != string(ChunkSetStatusReady) || activeSetID != second.ChunkSetID {
		t.Fatalf(
			"reconciled attempts: abandoned=%q chunks=%d winner=%q active=%q",
			abandonedStatus, abandonedChunks, winnerStatus, activeSetID,
		)
	}
}

func TestDeterministicParseFailurePublishesNothing(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service, client := newWorkingLibraryService(t, database, store, parserFunc(func(
		context.Context,
		string,
		string,
	) ([]ParsedChunk, error) {
		return nil, ErrInvalidParserData
	}))
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "invalid.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusFailed
	})
	if failed.ActiveChunkSetID != "" || failed.ErrorMessage == "" {
		t.Fatalf("failed file exposed a generation: %+v", failed)
	}
	var sets, chunks int
	var status string
	if err := database.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM library_chunk_set WHERE file_id = $1),
			(SELECT count(*) FROM library_chunk AS chunk JOIN library_chunk_set AS chunk_set ON chunk_set.id = chunk.chunk_set_id WHERE chunk_set.file_id = $1),
			(SELECT status FROM library_chunk_set WHERE file_id = $1 LIMIT 1)
	`, file.ID).Scan(&sets, &chunks, &status); err != nil {
		t.Fatal(err)
	}
	if sets != 1 || chunks != 0 || status != string(ChunkSetStatusFailed) {
		t.Fatalf("failed parse attempts=%d chunks=%d status=%q", sets, chunks, status)
	}
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestCancelledChunkWorkerCannotCommitTerminalFailure(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()
	var calls atomic.Int32
	parser := parserFunc(func(context.Context, string, string) ([]ParsedChunk, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			// The stale worker deliberately ignores cancellation long enough to
			// enter the terminal failure transaction after takeover.
			return nil, ErrInvalidParserData
		}
		return []ParsedChunk{{Content: "replacement generation"}}, nil
	})
	service, client := newWorkingLibraryServiceWithWorkers(t, database, store, parser, 1)
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "takeover.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first parser attempt did not start")
	}
	oldJob, found, err := latestLibraryJob(t.Context(), client, chunkArgs{}.Kind(), file.ID)
	if err != nil || !found || oldJob.State != rivertype.JobStateRunning {
		t.Fatalf("load running chunk job: state=%v found=%t error=%v", oldJob, found, err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE river_job
		SET attempted_at = now() - interval '1 hour'
		WHERE id = $1
	`, oldJob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE library_file
		SET updated_at = now() - interval '1 hour'
		WHERE id = $1
	`, file.ID); err != nil {
		t.Fatal(err)
	}
	service.staleDerivationAfter = time.Minute
	if err := service.reconcileStaleDerivations(t.Context()); err != nil {
		t.Fatal(err)
	}
	var oldState string
	if err := database.QueryRow(
		t.Context(), `SELECT state::text FROM river_job WHERE id = $1`, oldJob.ID,
	).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if oldState != string(rivertype.JobStateCancelled) {
		t.Fatalf("taken-over job state = %q, want cancelled", oldState)
	}
	close(releaseFirst)
	ready := waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady && current.ActiveChunkSetID != ""
	})
	if ready.ErrorMessage != "" || calls.Load() < 2 {
		t.Fatalf("replacement did not recover cancelled worker: file=%+v parser_calls=%d", ready, calls.Load())
	}
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestChangedFailureFenceSupersedesTerminalParseError(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	parser := &changingFailureFenceParser{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	parser.fence.Store("vision-secret:v1")
	service, client := newWorkingLibraryServiceWithWorkers(t, database, store, parser, 1)
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "fenced.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-parser.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first parser attempt did not start")
	}
	// Credential rotation does not change successful output identity, but it must
	// supersede a terminal error produced by the old credential snapshot.
	parser.fence.Store("vision-secret:v2")
	close(parser.release)
	ready := waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady && current.ActiveChunkSetID != ""
	})
	if ready.ErrorMessage != "" || parser.calls.Load() < 2 {
		t.Fatalf("replacement did not supersede stale failure: file=%+v parser_calls=%d", ready, parser.calls.Load())
	}
	var failedSets int
	if err := database.QueryRow(t.Context(), `
		SELECT count(*) FROM library_chunk_set WHERE file_id = $1 AND status = 'failed'
	`, file.ID).Scan(&failedSets); err != nil {
		t.Fatal(err)
	}
	if failedSets != 0 {
		t.Fatalf("stale terminal result created %d failed ChunkSets", failedSets)
	}
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestInactiveReadyChunkSetDoesNotChangePublishedGeneration(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := newWorkingLibraryService(t, database, store, staticLibraryParser{})
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "active.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady && current.ActiveChunkSetID != ""
	})
	inactiveID := uuid.NewString()
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk_set (
			id, file_id, derivation_key, processor_key, raw_sha256,
			status, chunk_count, content_digest, completed_at
		) VALUES ($1, $2, 'future-derivation', 'future-parser', $3, 'ready', 1, $4, now())
	`, inactiveID, file.ID, file.RawSHA256, bytes.Repeat([]byte{2}, sha256.Size)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk (id, chunk_set_id, ordinal, content, locator, content_sha256)
		VALUES ($1, $2, 0, 'inactive content', '{}', $3)
	`, uuid.NewString(), inactiveID, bytes.Repeat([]byte{3}, sha256.Size)); err != nil {
		t.Fatal(err)
	}
	var activeID string
	var activeChunkCount, inactiveChunkCount int
	if err := database.QueryRow(t.Context(), `
		SELECT
			file.active_chunk_set_id,
			count(chunk.id) FILTER (WHERE chunk.chunk_set_id = file.active_chunk_set_id),
			count(chunk.id) FILTER (WHERE chunk.chunk_set_id = $2)
		FROM library_file AS file
		LEFT JOIN library_chunk_set AS chunk_set ON chunk_set.file_id = file.id
		LEFT JOIN library_chunk AS chunk ON chunk.chunk_set_id = chunk_set.id
		WHERE file.id = $1
		GROUP BY file.active_chunk_set_id
	`, file.ID, inactiveID).Scan(&activeID, &activeChunkCount, &inactiveChunkCount); err != nil {
		t.Fatal(err)
	}
	if activeID != ready.ActiveChunkSetID || activeID == inactiveID || activeChunkCount == 0 || inactiveChunkCount != 1 {
		t.Fatalf(
			"publication pointer=%s ready=%s inactive=%s active_chunks=%d inactive_chunks=%d",
			activeID, ready.ActiveChunkSetID, inactiveID, activeChunkCount, inactiveChunkCount,
		)
	}
}

type parserFunc func(context.Context, string, string) ([]ParsedChunk, error)

func (parserFunc) Profile(context.Context, string) (string, error) { return testParserProfile, nil }

func (f parserFunc) Parse(ctx context.Context, path, mediaType, _ string) ([]ParsedChunk, error) {
	return f(ctx, path, mediaType)
}

type changingFailureFenceParser struct {
	fence   atomic.Value
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (*changingFailureFenceParser) Profile(context.Context, string) (string, error) {
	return testParserProfile, nil
}

func (p *changingFailureFenceParser) FailureFence(context.Context, string) (string, error) {
	return p.fence.Load().(string), nil
}

func (p *changingFailureFenceParser) Parse(context.Context, string, string, string) ([]ParsedChunk, error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
		<-p.release
		return nil, ErrInvalidParserData
	}
	return []ParsedChunk{{Content: "replacement generation"}}, nil
}

func newWorkingLibraryService(
	t *testing.T,
	database *pgxpool.Pool,
	store RawStore,
	parser Parser,
) (*Service, *river.Client[pgx.Tx]) {
	return newWorkingLibraryServiceWithWorkers(t, database, store, parser, 0)
}

func newWorkingLibraryServiceWithWorkers(
	t *testing.T,
	database *pgxpool.Pool,
	store RawStore,
	parser Parser,
	maxWorkers int,
) (*Service, *river.Client[pgx.Tx]) {
	t.Helper()
	service, err := NewService(ServiceConfig{
		DB: database, RawStore: store, Parser: parser,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TempDir: t.TempDir(),
		MaxConcurrentUploads: 4, MaxSpoolBytes: 4 * MaxFileBytes,
		ReconciliationInterval: time.Hour, MaxWorkers: maxWorkers,
	})
	if err != nil {
		t.Fatal(err)
	}
	workers := river.NewWorkers()
	service.RegisterRiverWorkers(workers)
	queue, queueConfig := service.QueueConfig()
	client, err := appdb.NewWorkingRiverClient(
		database,
		map[string]river.QueueConfig{queue: queueConfig},
		workers,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BindRiverClient(client); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Stop(ctx); err != nil {
			t.Errorf("stop working River client: %v", err)
		}
	})
	return service, client
}

func waitForLibraryFile(
	t *testing.T,
	service *Service,
	fileID string,
	condition func(LibraryFile) bool,
) LibraryFile {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last LibraryFile
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = service.Get(t.Context(), fileID)
		if lastErr == nil && condition(last) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("library file %s did not converge: last=%+v error=%v", fileID, last, lastErr)
	return LibraryFile{}
}

func assertLatestLibraryJobState(
	t *testing.T,
	client *river.Client[pgx.Tx],
	kind string,
	fileID string,
	want rivertype.JobState,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var state rivertype.JobState
	var found bool
	var err error
	for time.Now().Before(deadline) {
		state, found, err = latestLibraryJobState(t.Context(), client, kind, fileID)
		if err == nil && found && state == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("latest %s job for %s = %q, found=%t, error=%v; want %q", kind, fileID, state, found, err, want)
}

func mustLibraryDerivationKey(t *testing.T, rawSHA256 []byte, mediaType string) string {
	t.Helper()
	key, err := libraryDerivationKey(rawSHA256, mediaType, testParserProfile)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
