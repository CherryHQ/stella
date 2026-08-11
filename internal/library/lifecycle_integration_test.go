package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestDeleteTombstonesBeforeRawCleanupAndEventuallyHardDeletes(t *testing.T) {
	database := dbtest.New(t)
	baseStore, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	store := &gatedDeleteRawStore{
		RawStore: baseStore,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	service, client := newWorkingLibraryService(t, database, store, staticLibraryParser{})
	authority := testAuthority(t, testUserA, true)
	file, err := service.CreateManagedUpload(
		t.Context(), authority, ScopeSystem, "", "delete.txt", stringsReader("delete source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady
	})

	if err := service.DeleteManaged(t.Context(), authority, file.ID); err != nil {
		t.Fatalf("DeleteManaged: %v", err)
	}
	select {
	case <-store.started:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup worker did not begin deleting the raw snapshot")
	}
	if _, err := service.Get(t.Context(), file.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tombstoned file remained visible: %v", err)
	}
	quota, err := quotaForOwner(t.Context(), sqlc.New(database), Owner{Scope: ScopeSystem})
	if err != nil {
		t.Fatal(err)
	}
	if quota.UsedFiles != 0 || quota.UsedBytes != 0 {
		t.Fatalf("tombstoned file still consumes logical quota: %+v", quota)
	}
	var deletedAt *time.Time
	if err := database.QueryRow(
		t.Context(), `SELECT deleted_at FROM library_file WHERE id = $1`, file.ID,
	).Scan(&deletedAt); err != nil || deletedAt == nil {
		t.Fatalf("durable tombstone missing while raw cleanup is blocked: deleted_at=%v error=%v", deletedAt, err)
	}
	rawKey, err := RawKey(file.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := baseStore.Open(t.Context(), rawKey)
	if err != nil {
		t.Fatalf("raw disappeared before cleanup was released: %v", err)
	}
	_ = raw.Close()

	close(store.release)
	waitForLibraryMetadataAbsent(t, database, file.ID)
	if _, err := baseStore.Open(t.Context(), rawKey); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("raw exists after hard deletion: %v", err)
	}
	assertLatestLibraryJobState(t, client, cleanupArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestDeleteCancelsInFlightParsingWithoutResurrection(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var startedOnce sync.Once
	parser := parserFunc(func(ctx context.Context, _, _ string) ([]ParsedChunk, error) {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
	service, _ := newWorkingLibraryService(t, database, store, parser)
	authority := testAuthority(t, testUserA, true)
	file, err := service.CreateManagedUpload(
		t.Context(), authority, ScopeSystem, "", "in-flight.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("chunk worker did not enter the parser")
	}
	if err := service.DeleteManaged(t.Context(), authority, file.ID); err != nil {
		t.Fatal(err)
	}
	waitForLibraryMetadataAbsent(t, database, file.ID)
	var chunks int
	if err := database.QueryRow(t.Context(), `
		SELECT count(*)
		FROM library_chunk AS chunk
		JOIN library_chunk_set AS chunk_set ON chunk_set.id = chunk.chunk_set_id
		WHERE chunk_set.file_id = $1
	`, file.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("deleted in-flight generation published %d chunks", chunks)
	}
	rawKey, err := RawKey(file.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(t.Context(), rawKey); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted in-flight raw still exists: %v", err)
	}
}

func TestDeleteCancelsQueuedChunkWork(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	client := service.riverClient()
	authority := testAuthority(t, testUserA, true)
	file, err := service.CreateManagedUpload(
		t.Context(), authority, ScopeSystem, "", "queued.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteManaged(t.Context(), authority, file.ID); err != nil {
		t.Fatal(err)
	}
	state, found, err := latestLibraryJobState(t.Context(), client, chunkArgs{}.Kind(), file.ID)
	if err != nil || !found || state != rivertype.JobStateCancelled {
		t.Fatalf("queued chunk cancellation = state %q found=%t error=%v", state, found, err)
	}
	if _, err := service.Get(t.Context(), file.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("queued deletion remained visible: %v", err)
	}
	assertRawObjectExists(t, store, file.ID, true)
}

func TestCancellationFailureNeverReversesTombstone(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	closedPool, err := pgxpool.New(t.Context(), database.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	client, err := newInsertOnlyLibraryRiver(closedPool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		DB: database, RawStore: store, Parser: staticLibraryParser{}, River: client,
		Logger: discardLibraryLogger(), TempDir: t.TempDir(),
		MaxConcurrentUploads: 1, MaxSpoolBytes: MaxFileBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := testAuthority(t, testUserA, true)
	file, err := service.CreateManagedUpload(
		t.Context(), authority, ScopeSystem, "", "cancel-failure.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	// InsertTx uses the caller's transaction, so snapshot and cleanup jobs were
	// durable. Closing this client's own pool makes only the post-commit JobList
	// cancellation call fail.
	closedPool.Close()
	if err := service.DeleteManaged(t.Context(), authority, file.ID); err != nil {
		t.Fatalf("DeleteManaged returned a best-effort cancellation error: %v", err)
	}
	var tombstoned bool
	if err := database.QueryRow(
		t.Context(), `SELECT deleted_at IS NOT NULL FROM library_file WHERE id = $1`, file.ID,
	).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if !tombstoned {
		t.Fatal("cancellation failure reversed the committed tombstone")
	}
}

func TestCleanupRetriesAfterRawStoreFailure(t *testing.T) {
	database := dbtest.New(t)
	baseStore, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	store := &failOnceDeleteRawStore{RawStore: baseStore, failed: make(chan struct{})}
	service, client := newWorkingLibraryService(t, database, store, staticLibraryParser{})
	authority := testAuthority(t, testUserA, true)
	file, err := service.CreateManagedUpload(
		t.Context(), authority, ScopeSystem, "", "retry-delete.txt", stringsReader("source"),
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForLibraryFile(t, service, file.ID, func(current LibraryFile) bool {
		return current.Status == FileStatusReady
	})
	if err := service.DeleteManaged(t.Context(), authority, file.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.failed:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup did not encounter the injected RawStore failure")
	}
	var tombstoned bool
	if err := database.QueryRow(
		t.Context(), `SELECT deleted_at IS NOT NULL FROM library_file WHERE id = $1`, file.ID,
	).Scan(&tombstoned); err != nil || !tombstoned {
		t.Fatalf("failed cleanup lost tombstone: tombstoned=%t error=%v", tombstoned, err)
	}
	waitForLibraryMetadataAbsent(t, database, file.ID)
	if calls := store.calls.Load(); calls < 2 {
		t.Fatalf("cleanup Delete calls = %d, want a retry", calls)
	}
	assertLatestLibraryJobState(t, client, cleanupArgs{}.Kind(), file.ID, rivertype.JobStateCompleted)
}

func TestOrphanReconciliationIsAgeBoundedAndFailClosed(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	oldOrphanID := uuid.NewString()
	youngOrphanID := uuid.NewString()
	liveID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Now().UTC()
	tombstoneID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", &deletedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{oldOrphanID, youngOrphanID, liveID, tombstoneID}
	for _, id := range ids {
		key, keyErr := RawKey(id)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		if createErr := store.Create(t.Context(), key, bytes.NewBufferString(id)); createErr != nil {
			t.Fatal(createErr)
		}
	}
	malformedKey := RawPrefix + "/not-a-uuid/source"
	if err := store.Create(t.Context(), malformedKey, stringsReader("malformed")); err != nil {
		t.Fatal(err)
	}
	// Compact UUID text remains non-canonical while mapping to a distinct path
	// on both case-sensitive and case-insensitive filesystems.
	compactOwnedID := strings.ReplaceAll(liveID, "-", "")
	nonCanonicalOwnedKey := RawPrefix + "/" + compactOwnedID + "/source"
	if err := store.Create(t.Context(), nonCanonicalOwnedKey, stringsReader("non-canonical owner")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	for _, id := range []string{oldOrphanID, liveID, tombstoneID} {
		key, _ := RawKey(id)
		path, pathErr := store.path(key)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	malformedPath, err := store.path(malformedKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(malformedPath, old, old); err != nil {
		t.Fatal(err)
	}
	nonCanonicalOwnedPath, err := store.path(nonCanonicalOwnedKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(nonCanonicalOwnedPath, old, old); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListPage(t.Context(), RawPrefix, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.reconcileOrphanPage(t.Context(), page.Objects, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertRawObjectExists(t, store, oldOrphanID, false)
	assertRawObjectExists(t, store, youngOrphanID, true)
	assertRawObjectExists(t, store, liveID, true)
	assertRawObjectExists(t, store, tombstoneID, true)
	malformed, err := store.Open(t.Context(), malformedKey)
	if err != nil {
		t.Fatalf("malformed ownership was not retained fail-closed: %v", err)
	}
	_ = malformed.Close()
	nonCanonicalOwned, err := store.Open(t.Context(), nonCanonicalOwnedKey)
	if err != nil {
		t.Fatalf("non-canonical owned key was not retained fail-closed: %v", err)
	}
	_ = nonCanonicalOwned.Close()

	// An unavailable database must make reconciliation retain every candidate
	// rather than guessing that missing ownership means an orphan.
	uncertainID := uuid.NewString()
	key, _ := RawKey(uncertainID)
	if err := store.Create(t.Context(), key, stringsReader("uncertain")); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path(key)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	unavailablePool, err := pgxpool.New(t.Context(), database.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	unavailablePool.Close()
	service.q = sqlc.New(unavailablePool)
	err = service.reconcileOrphanPage(t.Context(), []RawObject{{
		Key: key, Size: 9, LastModified: old,
	}}, time.Now().UTC().Add(-time.Hour))
	if err == nil {
		t.Fatal("orphan reconciliation succeeded with an unavailable ownership query")
	}
	assertRawObjectExists(t, store, uncertainID, true)
}

func TestOrphanReconciliationSurvivesUploaderProcessRestart(t *testing.T) {
	database := dbtest.New(t)
	baseStore, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	const orphanMinAge = time.Second
	config := ServiceConfig{
		Parser:                   staticLibraryParser{},
		SnapshotCommitTimeout:    500 * time.Millisecond,
		DatabaseStatementTimeout: 200 * time.Millisecond,
		DatabaseLockTimeout:      100 * time.Millisecond,
		MaxClockSkew:             50 * time.Millisecond,
		OrphanSafetyMargin:       50 * time.Millisecond,
		OrphanMinAge:             orphanMinAge,
	}
	crashStore := &publishThenAbortRawStore{
		RawStore:  baseStore,
		published: make(chan string, 1),
	}
	config.RawStore = crashStore
	uploader := newLibraryServiceWithConfig(t, database, config)
	authority := testAuthority(t, testUserA, true)
	uploadCtx, cancelUpload := context.WithCancel(t.Context())
	uploadResult := make(chan error, 1)
	go func() {
		_, uploadErr := uploader.CreateManagedUpload(
			uploadCtx,
			authority,
			ScopeSystem,
			"",
			"restart.txt",
			stringsReader("raw published before uploader loss"),
		)
		uploadResult <- uploadErr
	}()

	var rawKey string
	select {
	case rawKey = <-crashStore.published:
	case <-time.After(10 * time.Second):
		t.Fatal("uploader did not publish the raw snapshot")
	}
	cancelUpload()
	select {
	case uploadErr := <-uploadResult:
		if !errors.Is(uploadErr, context.Canceled) {
			t.Fatalf("terminated uploader error = %v, want context cancellation", uploadErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("terminated uploader remained able to commit metadata")
	}

	fileID, err := FileIDFromRawKey(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	var owners int
	if err := database.QueryRow(
		t.Context(), `SELECT count(*) FROM library_file WHERE id = $1`, fileID,
	).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 0 {
		t.Fatalf("terminated uploader committed %d metadata owner rows", owners)
	}

	// A new Service represents the process that starts after the uploader has
	// terminated. It must retain the raw throughout the safe-age window.
	config.RawStore = baseStore
	restarted := newLibraryServiceWithConfig(t, database, config)
	if err := restarted.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRawObjectExists(t, baseStore, fileID, true)

	path, err := baseStore.path(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	strictlyOld := time.Now().UTC().Add(-orphanMinAge - time.Second)
	if err := os.Chtimes(path, strictlyOld, strictlyOld); err != nil {
		t.Fatal(err)
	}
	if err := restarted.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRawObjectExists(t, baseStore, fileID, false)
	if err := database.QueryRow(
		t.Context(), `SELECT count(*) FROM library_file WHERE id = $1`, fileID,
	).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 0 {
		t.Fatalf("old uploader committed metadata after GC: owners=%d", owners)
	}
}

func TestOrphanReconciliationBoundsEachJob(t *testing.T) {
	database := dbtest.New(t)
	_, service := newLibraryService(t, database)
	store := &endlessPagingRawStore{supportsOrphanCollection: true}
	service.rawStore = store
	if err := service.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.calls != orphanPagesPerJob {
		t.Fatalf("ListPage calls = %d, want %d", store.calls, orphanPagesPerJob)
	}
	for _, limit := range store.limits {
		if limit != orphanPageSize {
			t.Fatalf("ListPage limit = %d, want %d", limit, orphanPageSize)
		}
	}
	if err := service.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.calls != 2*orphanPagesPerJob {
		t.Fatalf("two bounded scans made %d calls, want %d", store.calls, 2*orphanPagesPerJob)
	}
	if store.cursors[0] != "" || store.cursors[orphanPagesPerJob] != "page-4" {
		t.Fatalf("periodic scan did not resume from its durable cursor: %v", store.cursors)
	}
}

func TestOrphanReconciliationEventuallyPassesFirstScanBudget(t *testing.T) {
	database := dbtest.New(t)
	old := time.Now().UTC().Add(-2 * time.Hour)
	objects := make([]RawObject, 0, orphanPageSize*orphanPagesPerJob+1)
	liveCount := orphanPageSize * orphanPagesPerJob
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_file (
			id, scope, file_name, media_type, size_bytes, raw_sha256, status
		)
		SELECT
			('00000000-0000-7000-8000-' || lpad(n::text, 12, '0'))::uuid,
			'system', 'live.txt', 'text/plain', 1, $2, 'ready'
		FROM generate_series(0, $1::integer - 1) AS series(n)
	`, liveCount, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	for index := range orphanPageSize * orphanPagesPerJob {
		id := fmt.Sprintf("00000000-0000-7000-8000-%012d", index)
		key, err := RawKey(id)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, RawObject{Key: key, Size: 1, LastModified: old})
	}
	orphanID := "ffffffff-ffff-7fff-bfff-ffffffffffff"
	orphanKey, err := RawKey(orphanID)
	if err != nil {
		t.Fatal(err)
	}
	objects = append(objects, RawObject{Key: orphanKey, Size: 1, LastModified: old})
	store := &memoryPagedRawStore{
		supportsOrphanCollection: true,
		objects:                  objects,
		deleted:                  make(map[string]struct{}),
	}
	service := newLibraryServiceWithConfig(t, database, ServiceConfig{
		RawStore: store, Parser: staticLibraryParser{},
	})
	if err := service.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.wasDeleted(orphanKey) {
		t.Fatal("orphan beyond the first bounded scan was deleted too early")
	}
	if err := service.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !store.wasDeleted(orphanKey) {
		t.Fatal("orphan beyond the first bounded scan was never reached")
	}
	setting, err := sqlc.New(database).GetSetting(t.Context(), orphanCursorSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Value != "" {
		t.Fatalf("completed orphan scan cursor = %q, want wrapped head", setting.Value)
	}
}

func TestOrphanReconciliationSkipsStoreWithoutExclusiveNamespace(t *testing.T) {
	database := dbtest.New(t)
	store := &memoryPagedRawStore{
		objects: []RawObject{{
			Key:          RawPrefix + "/ffffffff-ffff-7fff-bfff-ffffffffffff/source",
			LastModified: time.Now().UTC().Add(-2 * time.Hour),
		}},
		deleted: make(map[string]struct{}),
	}
	service := newLibraryServiceWithConfig(t, database, ServiceConfig{
		RawStore: store, Parser: staticLibraryParser{},
	})
	if err := service.reconcileOrphanPages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.calls != 0 || len(store.deleted) != 0 {
		t.Fatalf("unowned RawStore was enumerated or mutated: calls=%d deleted=%d", store.calls, len(store.deleted))
	}
}

func TestReconciliationRepairsMissingJobsAndFinalizesDiscardedGeneration(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	client := service.riverClient()
	missingID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	discardedID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	stuckID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	recentRunningID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{missingID, discardedID, stuckID, recentRunningID} {
		if _, err := database.Exec(
			t.Context(), `UPDATE library_file SET updated_at = now() - interval '1 hour' WHERE id = $1`, id,
		); err != nil {
			t.Fatal(err)
		}
	}
	args := chunkArgs{FileID: discardedID}
	options := args.InsertOpts()
	inserted, err := client.Insert(t.Context(), args, &options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE river_job
		SET state = 'discarded', finalized_at = now()
		WHERE id = $1
	`, inserted.Job.ID); err != nil {
		t.Fatal(err)
	}
	stuckArgs := chunkArgs{FileID: stuckID}
	stuckOptions := stuckArgs.InsertOpts()
	stuckJob, err := client.Insert(t.Context(), stuckArgs, &stuckOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE river_job
		SET state = 'running', attempt = 1, attempted_at = now() - interval '1 hour'
		WHERE id = $1
	`, stuckJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	recentArgs := chunkArgs{FileID: recentRunningID}
	recentOptions := recentArgs.InsertOpts()
	recentJob, err := client.Insert(t.Context(), recentArgs, &recentOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE river_job
		SET state = 'running', attempt = 1, attempted_at = now()
		WHERE id = $1
	`, recentJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	// A fresh Service models the durable state seen after the worker process that
	// owned stuckJob was lost.
	restarted := newLibraryServiceWithConfig(t, database, ServiceConfig{
		RawStore: store,
		Parser:   staticLibraryParser{},
	})
	if err := restarted.reconcileStaleDerivations(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, found, err := latestLibraryJobState(t.Context(), client, chunkArgs{}.Kind(), missingID)
	if err != nil || !found || state != rivertype.JobStateAvailable {
		t.Fatalf("missing derivation repair = state %q found=%t error=%v", state, found, err)
	}
	failed, err := service.Get(t.Context(), discardedID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != FileStatusFailed || failed.ErrorMessage == "" {
		t.Fatalf("discarded generation did not converge to failed: %+v", failed)
	}
	var stuckState string
	var cancellationMarked bool
	if err := database.QueryRow(t.Context(), `
		SELECT state::text, metadata ? 'cancel_attempted_at'
		FROM river_job
		WHERE id = $1
	`, stuckJob.Job.ID).Scan(&stuckState, &cancellationMarked); err != nil {
		t.Fatal(err)
	}
	if stuckState != string(rivertype.JobStateCancelled) || !cancellationMarked {
		t.Fatalf("stuck running recovery = state %q cancellation_marked=%t", stuckState, cancellationMarked)
	}
	var recentState string
	var recentCancellationMarked bool
	if err := database.QueryRow(t.Context(), `
		SELECT state::text, metadata ? 'cancel_attempted_at'
		FROM river_job
		WHERE id = $1
	`, recentJob.Job.ID).Scan(&recentState, &recentCancellationMarked); err != nil {
		t.Fatal(err)
	}
	if recentState != string(rivertype.JobStateRunning) || recentCancellationMarked {
		t.Fatalf(
			"recent running job was taken over = state %q cancellation_marked=%t",
			recentState,
			recentCancellationMarked,
		)
	}
	state, found, err = latestLibraryJobState(t.Context(), client, chunkArgs{}.Kind(), stuckID)
	if err != nil || !found || state != rivertype.JobStateAvailable {
		t.Fatalf("local derivation takeover = state %q found=%t error=%v", state, found, err)
	}

	tombstoneID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "ready", &time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}
	stuckTombstoneID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "ready", &time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := cleanupArgs{FileID: stuckTombstoneID}
	cleanupOptions := cleanup.InsertOpts()
	stuckCleanupJob, err := client.Insert(t.Context(), cleanup, &cleanupOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE river_job
		SET state = 'running', attempt = 1, attempted_at = now() - interval '1 hour'
		WHERE id = $1
	`, stuckCleanupJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE library_file
		SET deleted_at = now() - interval '1 hour', updated_at = now() - interval '1 hour'
		WHERE id = $1
	`, stuckTombstoneID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.reconcileTombstones(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, found, err = latestLibraryJobState(t.Context(), client, cleanupArgs{}.Kind(), tombstoneID)
	if err != nil || !found || state != rivertype.JobStateAvailable {
		t.Fatalf("tombstone repair = state %q found=%t error=%v", state, found, err)
	}
	state, found, err = latestLibraryJobState(t.Context(), client, cleanupArgs{}.Kind(), stuckTombstoneID)
	if err != nil || !found || state != rivertype.JobStateAvailable {
		t.Fatalf("local cleanup takeover = state %q found=%t error=%v", state, found, err)
	}
	var stuckCleanupState string
	var cleanupCancellationMarked bool
	if err := database.QueryRow(t.Context(), `
		SELECT state::text, metadata ? 'cancel_attempted_at'
		FROM river_job
		WHERE id = $1
	`, stuckCleanupJob.Job.ID).Scan(&stuckCleanupState, &cleanupCancellationMarked); err != nil {
		t.Fatal(err)
	}
	if stuckCleanupState != string(rivertype.JobStateCancelled) || !cleanupCancellationMarked {
		t.Fatalf(
			"stuck cleanup recovery = state %q cancellation_marked=%t",
			stuckCleanupState,
			cleanupCancellationMarked,
		)
	}
}

func TestReconciliationRetiresSupersededBuildingGeneration(t *testing.T) {
	database := dbtest.New(t)
	_, service := newLibraryService(t, database)
	fileID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "ready", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawSHA256 := bytes.Repeat([]byte{1}, 32)
	currentKey, err := libraryDerivationKey(rawSHA256, "text/plain", testParserProfile)
	if err != nil {
		t.Fatal(err)
	}
	oldProfile := "test-parser:old"
	oldKey, err := libraryDerivationKey(rawSHA256, "text/plain", oldProfile)
	if err != nil {
		t.Fatal(err)
	}
	currentSetID := uuid.NewString()
	oldSetID := uuid.NewString()
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk_set (
			id, file_id, derivation_key, processor_key, raw_sha256,
			status, chunk_count, content_digest, completed_at
		) VALUES ($1, $2, $3, $4, $5, 'ready', 0, $6, now())
	`, currentSetID, fileID, currentKey, testParserProfile, rawSHA256, bytes.Repeat([]byte{2}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk_set (
			id, file_id, derivation_key, processor_key, raw_sha256, status
		) VALUES ($1, $2, $3, $4, $5, 'building')
	`, oldSetID, fileID, oldKey, oldProfile, rawSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk (id, chunk_set_id, ordinal, content, locator, content_sha256)
		VALUES ($1, $2, 0, 'partial old generation', '{}', $3)
	`, uuid.NewString(), oldSetID, bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(t.Context(), `
		UPDATE library_file
		SET active_chunk_set_id = $1, updated_at = now() - interval '1 hour'
		WHERE id = $2
	`, currentSetID, fileID); err != nil {
		t.Fatal(err)
	}
	if err := service.reconcileStaleDerivations(t.Context()); err != nil {
		t.Fatal(err)
	}
	var oldStatus, currentStatus, activeSetID string
	var oldError *string
	var oldChunks int
	if err := database.QueryRow(t.Context(), `
		SELECT
			old_set.status,
			old_set.error_message,
			current_set.status,
			file.active_chunk_set_id,
			(SELECT count(*) FROM library_chunk WHERE chunk_set_id = old_set.id)
		FROM library_file AS file
		JOIN library_chunk_set AS old_set ON old_set.id = $2
		JOIN library_chunk_set AS current_set ON current_set.id = $3
		WHERE file.id = $1
	`, fileID, oldSetID, currentSetID).Scan(
		&oldStatus, &oldError, &currentStatus, &activeSetID, &oldChunks,
	); err != nil {
		t.Fatal(err)
	}
	if oldStatus != string(ChunkSetStatusFailed) || oldError == nil || *oldError == "" || oldChunks != 0 {
		t.Fatalf("superseded generation status=%q error=%v chunks=%d", oldStatus, oldError, oldChunks)
	}
	if currentStatus != string(ChunkSetStatusReady) || activeSetID != currentSetID {
		t.Fatalf("active generation changed: status=%q active=%q want=%q", currentStatus, activeSetID, currentSetID)
	}
	rows, err := sqlc.New(database).ListStaleLibraryDerivation(
		t.Context(),
		sqlc.ListStaleLibraryDerivationParams{StaleBefore: time.Now().UTC(), Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("superseded generation still matched reconciliation: %+v", rows)
	}
	if _, found, err := latestLibraryJob(t.Context(), service.riverClient(), chunkArgs{}.Kind(), fileID); err != nil || found {
		t.Fatalf("ready file received replacement job: found=%t error=%v", found, err)
	}
}

func TestChunkStagingRowLockWaitIsBounded(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service := newLibraryServiceWithConfig(t, database, ServiceConfig{
		RawStore:                 store,
		Parser:                   staticLibraryParser{},
		SnapshotCommitTimeout:    500 * time.Millisecond,
		DatabaseStatementTimeout: 200 * time.Millisecond,
		DatabaseLockTimeout:      100 * time.Millisecond,
		MaxClockSkew:             10 * time.Millisecond,
		OrphanSafetyMargin:       10 * time.Millisecond,
		OrphanMinAge:             time.Second,
	})
	file, err := service.createSnapshot(
		t.Context(), Owner{Scope: ScopeSystem}, "row-lock.txt", stringsReader("content"),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, action, err := service.prepareChunkGeneration(t.Context(), file.ID)
	if err != nil || action != generationBuild {
		t.Fatalf("prepare generation = action %d error %v", action, err)
	}
	chunks, _, err := normalizeParsedChunks([]ParsedChunk{{Content: "bounded row lock"}})
	if err != nil {
		t.Fatal(err)
	}
	lockTx, err := database.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(context.Background()) }()
	if _, err := lockTx.Exec(
		t.Context(), `SELECT id FROM library_file WHERE id = $1 FOR UPDATE`, file.ID,
	); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = service.stageChunkBatch(t.Context(), target, chunks)
	if err == nil {
		t.Fatal("chunk staging ignored a held file row lock")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("row lock wait was not bounded: %s", elapsed)
	}
	var count int
	if err := database.QueryRow(
		t.Context(), `SELECT count(*) FROM library_chunk WHERE chunk_set_id = $1`, target.ChunkSetID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("timed-out staging transaction wrote %d chunks", count)
	}
}

func TestOrphanMinimumAgeMustExceedCommitUncertaintyWindow(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(ServiceConfig{
		DB:                       database,
		RawStore:                 store,
		Parser:                   staticLibraryParser{},
		MaxConcurrentUploads:     1,
		MaxSpoolBytes:            MaxFileBytes,
		SnapshotCommitTimeout:    5 * time.Second,
		DatabaseStatementTimeout: 4 * time.Second,
		DatabaseLockTimeout:      3 * time.Second,
		MaxClockSkew:             2 * time.Second,
		OrphanSafetyMargin:       time.Second,
		OrphanMinAge:             8 * time.Second,
	})
	if err == nil {
		t.Fatal("orphan age equal to commit window plus skew and margin was accepted")
	}
}

type gatedDeleteRawStore struct {
	RawStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type publishThenAbortRawStore struct {
	RawStore
	published chan string
}

type failOnceDeleteRawStore struct {
	RawStore
	failed chan struct{}
	calls  atomic.Int32
}

type endlessPagingRawStore struct {
	supportsOrphanCollection bool
	calls                    int
	limits                   []int
	cursors                  []string
}

func (*endlessPagingRawStore) Create(context.Context, string, io.Reader) error { return nil }

func (*endlessPagingRawStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, fs.ErrNotExist
}

func (*endlessPagingRawStore) Delete(context.Context, string) error { return nil }

func (s *endlessPagingRawStore) SupportsOrphanCollection() bool {
	return s.supportsOrphanCollection
}

func (s *endlessPagingRawStore) ListPage(
	_ context.Context,
	_ string,
	cursor string,
	limit int,
) (RawPage, error) {
	s.calls++
	s.limits = append(s.limits, limit)
	s.cursors = append(s.cursors, cursor)
	return RawPage{NextCursor: "page-" + string(rune('0'+s.calls))}, nil
}

type memoryPagedRawStore struct {
	supportsOrphanCollection bool
	objects                  []RawObject
	deleted                  map[string]struct{}
	calls                    int
}

func (*memoryPagedRawStore) Create(context.Context, string, io.Reader) error { return nil }

func (*memoryPagedRawStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, fs.ErrNotExist
}

func (s *memoryPagedRawStore) Delete(_ context.Context, key string) error {
	s.deleted[key] = struct{}{}
	return nil
}

func (s *memoryPagedRawStore) SupportsOrphanCollection() bool {
	return s.supportsOrphanCollection
}

func (s *memoryPagedRawStore) ListPage(
	_ context.Context,
	prefix string,
	cursor string,
	limit int,
) (RawPage, error) {
	s.calls++
	objects := make([]RawObject, 0, limit+1)
	for _, object := range s.objects {
		if object.Key <= cursor || !strings.HasPrefix(object.Key, prefix+"/") {
			continue
		}
		if _, deleted := s.deleted[object.Key]; deleted {
			continue
		}
		objects = append(objects, object)
		if len(objects) == limit+1 {
			break
		}
	}
	if len(objects) <= limit {
		return RawPage{Objects: objects}, nil
	}
	return RawPage{Objects: objects[:limit], NextCursor: objects[limit-1].Key}, nil
}

func (s *memoryPagedRawStore) wasDeleted(key string) bool {
	_, ok := s.deleted[key]
	return ok
}

func (s *failOnceDeleteRawStore) Delete(ctx context.Context, key string) error {
	if s.calls.Add(1) == 1 {
		close(s.failed)
		return errors.New("injected RawStore delete failure")
	}
	return s.RawStore.Delete(ctx, key)
}

func (s *gatedDeleteRawStore) Delete(ctx context.Context, key string) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.RawStore.Delete(ctx, key)
}

func (s *publishThenAbortRawStore) Create(ctx context.Context, key string, reader io.Reader) error {
	if err := s.RawStore.Create(ctx, key, reader); err != nil {
		return err
	}
	s.published <- key
	<-ctx.Done()
	return ctx.Err()
}

func waitForLibraryMetadataAbsent(t *testing.T, database *pgxpool.Pool, fileID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var count int
	var err error
	for time.Now().Before(deadline) {
		err = database.QueryRow(
			t.Context(), `SELECT count(*) FROM library_file WHERE id = $1`, fileID,
		).Scan(&count)
		if err == nil && count == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("library metadata %s remains: count=%d error=%v", fileID, count, err)
}

func assertRawObjectExists(t *testing.T, store *FSRawStore, fileID string, want bool) {
	t.Helper()
	key, err := RawKey(fileID)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(t.Context(), key)
	if err == nil {
		_ = object.Close()
	}
	switch {
	case want && err != nil:
		t.Fatalf("raw %s should exist: %v", fileID, err)
	case !want && !errors.Is(err, fs.ErrNotExist):
		t.Fatalf("raw %s should be absent: %v", fileID, err)
	}
}

func newLibraryServiceWithConfig(
	t *testing.T,
	database *pgxpool.Pool,
	config ServiceConfig,
) *Service {
	t.Helper()
	client, err := newInsertOnlyLibraryRiver(database)
	if err != nil {
		t.Fatal(err)
	}
	config.DB = database
	config.River = client
	config.Logger = discardLibraryLogger()
	config.TempDir = t.TempDir()
	config.MaxConcurrentUploads = 1
	config.MaxSpoolBytes = MaxFileBytes
	service, err := NewService(config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newInsertOnlyLibraryRiver(database *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(database), &river.Config{})
}

func discardLibraryLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
