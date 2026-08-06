package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	set, err := sqlc.New(database).GetLibraryChunkSetByDerivation(
		t.Context(),
		sqlc.GetLibraryChunkSetByDerivationParams{
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
	rows, err := sqlc.New(database).ListLibraryChunkByOrdinals(
		t.Context(),
		sqlc.ListLibraryChunkByOrdinalsParams{
			ChunkSetID: set.ID,
			Ordinals:   []int64{0, 1, 2},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyStagedBatch(staged, rows); err != nil {
		t.Fatalf("persisted chunks differ from parser output: %v", err)
	}
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestChunkStagingIsIdempotentAndInvisibleUntilPublication(t *testing.T) {
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
	if err := service.stageChunkBatch(t.Context(), target, chunks); err != nil {
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
	if restartedTarget.ChunkSetID != target.ChunkSetID || restartedTarget.DerivationKey != target.DerivationKey {
		t.Fatalf("restart changed deterministic generation: before=%+v after=%+v", target, restartedTarget)
	}
	if err := restarted.stageChunkBatch(t.Context(), restartedTarget, chunks); err != nil {
		t.Fatalf("idempotent restaging failed: %v", err)
	}
	var count int
	var activeSetID *string
	if err := database.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1),
			active_chunk_set_id
		FROM library_file
		WHERE id = $2
	`, target.ChunkSetID, file.ID).Scan(&count, &activeSetID); err != nil {
		t.Fatal(err)
	}
	if count != len(chunks) || activeSetID != nil {
		t.Fatalf("partial staging count = %d, active pointer = %v", count, activeSetID)
	}

	conflict := append([]stagedChunk(nil), chunks...)
	conflict[0].Content = "different content"
	conflict[0].ContentSHA256 = sha256.Sum256([]byte(conflict[0].Content))
	if err := restarted.stageChunkBatch(t.Context(), restartedTarget, conflict[:1]); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("conflicting restaging error = %v, want ErrGenerationConflict", err)
	}
	// Both insert-only River clients are deliberately not started. No background
	// worker can race this test's direct staging assertions.
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
		return nil, ErrInvalidXbergJSON
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
	set, err := sqlc.New(database).GetLibraryChunkSetByDerivation(
		t.Context(),
		sqlc.GetLibraryChunkSetByDerivationParams{
			FileID: file.ID, DerivationKey: mustLibraryDerivationKey(t, file.RawSHA256, file.MediaType),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ChunkSetStatus(set.Status) != ChunkSetStatusFailed {
		t.Fatalf("failed parser left ChunkSet status %q", set.Status)
	}
	if !set.UpdatedAt.After(set.CreatedAt) {
		t.Fatalf(
			"failed ChunkSet updated_at = %s, want after created_at %s",
			set.UpdatedAt,
			set.CreatedAt,
		)
	}
	var chunks int
	if err := database.QueryRow(
		t.Context(), `SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1`, set.ID,
	).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("failed generation retained %d published candidates", chunks)
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

func (f parserFunc) Parse(ctx context.Context, path, mediaType string) ([]ParsedChunk, error) {
	return f(ctx, path, mediaType)
}

func newWorkingLibraryService(
	t *testing.T,
	database *pgxpool.Pool,
	store RawStore,
	parser Parser,
) (*Service, *river.Client[pgx.Tx]) {
	t.Helper()
	service, err := NewService(ServiceConfig{
		DB: database, RawStore: store, Parser: parser,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TempDir: t.TempDir(),
		MaxConcurrentUploads: 4, MaxSpoolBytes: 4 * MaxFileBytes,
		ReconciliationInterval: time.Hour,
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
	key, err := libraryDerivationKey(rawSHA256, mediaType)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
