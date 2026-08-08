package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

const (
	testUserA         = "10000000-0000-0000-0000-000000000001"
	testUserB         = "10000000-0000-0000-0000-000000000002"
	testAgentA        = "library-agent-a"
	testParserProfile = "test-parser:v1"
)

func TestLibrarySchemaEnforcesOwnerAndActivePointer(t *testing.T) {
	database := dbtest.New(t)
	seedLibraryPrincipals(t, database)
	valid := []Owner{
		{Scope: ScopeSystem},
		{Scope: ScopeSystemAgent, AgentID: testAgentA},
		{Scope: ScopeUser, UserID: testUserA},
		{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA},
	}
	for index, owner := range valid {
		if _, err := insertLibraryFile(t.Context(), database, owner, int64(index), "processing", nil); err != nil {
			t.Fatalf("valid owner %+v: %v", owner, err)
		}
	}
	invalid := []Owner{
		{Scope: ScopeSystem, UserID: testUserA},
		{Scope: ScopeSystem, AgentID: testAgentA},
		{Scope: ScopeSystemAgent},
		{Scope: ScopeSystemAgent, UserID: testUserA, AgentID: testAgentA},
		{Scope: ScopeUser},
		{Scope: ScopeUser, UserID: testUserA, AgentID: testAgentA},
		{Scope: ScopeUserAgent, UserID: testUserA},
		{Scope: "future_scope"},
	}
	for _, owner := range invalid {
		if _, err := insertLibraryFile(t.Context(), database, owner, 1, "processing", nil); err == nil {
			t.Fatalf("invalid owner %+v passed database CHECK", owner)
		}
	}
	if _, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, -1, "processing", nil,
	); err == nil {
		t.Fatal("negative size passed database CHECK")
	}
	// Status is deliberately a Go-validated evolvable value, not a database
	// enum; adding a lifecycle state should not require dropping a CHECK first.
	if _, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "future_status", nil,
	); err != nil {
		t.Fatalf("evolvable status was rejected: %v", err)
	}

	fileA, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fileB, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	setA := uuid.NewString()
	setB := uuid.NewString()
	for _, value := range []struct{ setID, fileID string }{{setA, fileA}, {setB, fileB}} {
		if _, err := database.Exec(t.Context(), `
			INSERT INTO library_chunk_set
				(id, file_id, derivation_key, processor_key, raw_sha256)
			VALUES ($1, $2, $3, 'test-parser:v1', $4)
		`, value.setID, value.fileID, "derivation:"+value.setID, bytes.Repeat([]byte{1}, sha256.Size)); err != nil {
			t.Fatalf("insert ChunkSet: %v", err)
		}
	}
	if _, err := database.Exec(
		t.Context(), `UPDATE library_file SET active_chunk_set_id = $1 WHERE id = $2`, setB, fileA,
	); err == nil {
		t.Fatal("active pointer accepted another file's ChunkSet")
	}
	if _, err := database.Exec(
		t.Context(), `UPDATE library_file SET active_chunk_set_id = $1 WHERE id = $2`, setA, fileA,
	); err != nil {
		t.Fatalf("same-file active pointer was rejected: %v", err)
	}
	if _, err := database.Exec(t.Context(), `DELETE FROM library_file WHERE id = $1`, fileA); err != nil {
		t.Fatalf("file deletion did not cascade through its active ChunkSet: %v", err)
	}
}

func TestCreateManagedUploadCommitsRawMetadataAndUniqueJob(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	content := []byte("travel reimbursement limit is 800 yuan")
	file, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", "travel.txt", bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("CreateManagedUpload: %v", err)
	}
	parsedID, err := uuid.Parse(file.ID)
	if err != nil || parsedID.Version() != 7 {
		t.Fatalf("file ID %q is not UUIDv7: %v", file.ID, err)
	}
	if file.Status != FileStatusProcessing || file.SizeBytes != int64(len(content)) {
		t.Fatalf("created file = %+v", file)
	}
	wantHash := sha256.Sum256(content)
	if !bytes.Equal(file.RawSHA256, wantHash[:]) {
		t.Fatalf("raw hash = %x, want %x", file.RawSHA256, wantHash)
	}
	rawKey, err := RawKey(file.ID)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(t.Context(), rawKey)
	if err != nil {
		t.Fatal(err)
	}
	gotContent, readErr := io.ReadAll(object)
	_ = object.Close()
	if readErr != nil || !bytes.Equal(gotContent, content) {
		t.Fatalf("raw content = %q, error %v", gotContent, readErr)
	}
	var kind, fileID string
	if err := database.QueryRow(t.Context(), `
		SELECT kind, args ->> 'file_id'
		FROM river_job
		WHERE kind = 'stella_library_chunk'
	`).Scan(&kind, &fileID); err != nil {
		t.Fatalf("load River job: %v", err)
	}
	if kind != (chunkArgs{}).Kind() || fileID != file.ID {
		t.Fatalf("River job = %q %q, want %q %q", kind, fileID, (chunkArgs{}).Kind(), file.ID)
	}
	var rawColumnCount int
	if err := database.QueryRow(t.Context(), `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_name = 'library_file' AND column_name = 'raw_content'
	`).Scan(&rawColumnCount); err != nil {
		t.Fatal(err)
	}
	if rawColumnCount != 0 {
		t.Fatal("raw_content BYTEA still exists in library_file")
	}
}

func TestNewServiceRequiresExplicitParserProfile(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(ServiceConfig{
		DB: database, RawStore: store, Parser: staticLibraryParser{},
	})
	if err == nil || !strings.Contains(err.Error(), "parser profile is required") {
		t.Fatalf("NewService error = %v, want missing parser profile", err)
	}
}

func TestCreateManagedUploadAuthorizesBeforeReadingBody(t *testing.T) {
	database := dbtest.New(t)
	_, service := newLibraryService(t, database)
	reader := &countingReader{reader: stringsReader("secret")}
	_, err := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, false), ScopeSystem, "", "secret.txt", reader,
	)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateManagedUpload error = %v, want ErrForbidden", err)
	}
	if reader.reads != 0 {
		t.Fatalf("unauthorized request body was read %d times", reader.reads)
	}
}

func TestRawStoreIOCompletesBeforeDatabaseTransactionBegins(t *testing.T) {
	database := dbtest.New(t)
	baseStore, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingRawStore{
		RawStore: baseStore, started: make(chan struct{}), release: make(chan struct{}),
	}
	client, err := river.NewClient(riverpgxv5.New(database), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		DB: database, RawStore: blocking, Parser: staticLibraryParser{}, ParserProfile: testParserProfile, River: client,
		TempDir: t.TempDir(), MaxConcurrentUploads: 1, MaxSpoolBytes: MaxFileBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, createErr := service.createSnapshot(
			t.Context(), Owner{Scope: ScopeSystem}, "blocked.txt", stringsReader("content"),
		)
		result <- createErr
	}()
	<-blocking.started
	var idleTransactions int
	if err := database.QueryRow(t.Context(), `
		SELECT count(*)
		FROM pg_stat_activity
		WHERE datname = current_database()
		  AND pid <> pg_backend_pid()
		  AND state = 'idle in transaction'
	`).Scan(&idleTransactions); err != nil {
		close(blocking.release)
		t.Fatal(err)
	}
	if idleTransactions != 0 {
		close(blocking.release)
		t.Fatalf("RawStore Create overlapped %d idle database transactions", idleTransactions)
	}
	close(blocking.release)
	if err := <-result; err != nil {
		t.Fatalf("createSnapshot after release: %v", err)
	}
}

func TestSnapshotDeadlineBoundsRawStoreCreate(t *testing.T) {
	database := dbtest.New(t)
	baseStore, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingRawStore{
		RawStore: baseStore, started: make(chan struct{}), release: make(chan struct{}),
	}
	service := newSnapshotServiceWithConfig(t, database, ServiceConfig{
		RawStore:                 blocking,
		SnapshotCommitTimeout:    80 * time.Millisecond,
		DatabaseStatementTimeout: 60 * time.Millisecond,
		DatabaseLockTimeout:      40 * time.Millisecond,
	})
	started := time.Now()
	_, err = service.createSnapshot(
		context.Background(), Owner{Scope: ScopeSystem}, "blocked-raw.txt", stringsReader("content"),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("raw publication deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("raw publication was not bounded: %s", elapsed)
	}
	page, listErr := baseStore.ListPage(t.Context(), RawPrefix, "", 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("timed-out raw publication left %d objects", len(page.Objects))
	}
}

func TestSnapshotCommitLockWaitIsBoundedAndCompensated(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service := newSnapshotServiceWithConfig(t, database, ServiceConfig{
		RawStore:                 store,
		SnapshotCommitTimeout:    300 * time.Millisecond,
		DatabaseStatementTimeout: 200 * time.Millisecond,
		DatabaseLockTimeout:      100 * time.Millisecond,
	})
	lockTx, err := database.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(context.Background()) }()
	if _, err := lockTx.Exec(
		t.Context(), `SELECT pg_advisory_xact_lock($1)`, quotaLockKey(Owner{Scope: ScopeSystem}),
	); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = service.createSnapshot(
		t.Context(), Owner{Scope: ScopeSystem}, "blocked-commit.txt", stringsReader("content"),
	)
	if err == nil {
		t.Fatal("snapshot commit unexpectedly waited through a held quota lock")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("snapshot lock wait was not bounded: %s", elapsed)
	}
	page, listErr := store.ListPage(t.Context(), RawPrefix, "", 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("known pre-commit timeout left %d raw objects", len(page.Objects))
	}
}

func TestSnapshotCommitRespectsEarlierRequestDeadline(t *testing.T) {
	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service := newSnapshotServiceWithConfig(t, database, ServiceConfig{
		RawStore:                 store,
		SnapshotCommitTimeout:    2 * time.Second,
		DatabaseStatementTimeout: time.Second,
		DatabaseLockTimeout:      time.Second,
	})
	lockTx, err := database.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(context.Background()) }()
	if _, err := lockTx.Exec(
		t.Context(), `SELECT pg_advisory_xact_lock($1)`, quotaLockKey(Owner{Scope: ScopeSystem}),
	); err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = service.createSnapshot(
		requestContext, Owner{Scope: ScopeSystem}, "request-timeout.txt", stringsReader("content"),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request deadline did not bound snapshot commit: %s", elapsed)
	}
	page, listErr := store.ListPage(t.Context(), RawPrefix, "", 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("request timeout left %d raw objects", len(page.Objects))
	}
}

func TestSnapshotCommitStatementTimeoutIsBoundedAndCompensated(t *testing.T) {
	database := dbtest.New(t)
	if _, err := database.Exec(t.Context(), `
		CREATE FUNCTION test_slow_library_insert() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_sleep(0.2);
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER test_slow_library_insert
		BEFORE INSERT ON library_file
		FOR EACH ROW EXECUTE FUNCTION test_slow_library_insert();
	`); err != nil {
		t.Fatal(err)
	}
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service := newSnapshotServiceWithConfig(t, database, ServiceConfig{
		RawStore:                 store,
		SnapshotCommitTimeout:    500 * time.Millisecond,
		DatabaseStatementTimeout: 50 * time.Millisecond,
		DatabaseLockTimeout:      40 * time.Millisecond,
	})
	started := time.Now()
	_, err = service.createSnapshot(
		t.Context(), Owner{Scope: ScopeSystem}, "statement-timeout.txt", stringsReader("content"),
	)
	if err == nil {
		t.Fatal("slow metadata insert ignored statement_timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("statement timeout was not bounded: %s", elapsed)
	}
	page, listErr := store.ListPage(t.Context(), RawPrefix, "", 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("statement timeout left %d raw objects", len(page.Objects))
	}
}

func TestCreateSnapshotCompensatesKnownPreCommitFailure(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	// River insertion occurs after metadata insertion but in the same
	// transaction. Removing its table makes that pre-commit phase fail.
	if _, err := database.Exec(t.Context(), `ALTER TABLE river_job RENAME TO river_job_unavailable`); err != nil {
		t.Fatal(err)
	}
	_, err := service.createSnapshot(
		t.Context(), Owner{Scope: ScopeSystem}, "rollback.txt", stringsReader("content"),
	)
	if err == nil {
		t.Fatal("createSnapshot unexpectedly succeeded without river_job")
	}
	var fileCount int
	if err := database.QueryRow(t.Context(), `SELECT count(*) FROM library_file`).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 0 {
		t.Fatalf("library_file count = %d after transaction rollback", fileCount)
	}
	page, err := store.ListPage(t.Context(), RawPrefix, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("known pre-commit failure left %d raw objects", len(page.Objects))
	}
}

func TestCommitOutcomeUnknownRetainsPotentiallyOwnedRaw(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	service.commitTx = func(ctx context.Context, transaction pgx.Tx) error {
		if err := transaction.Commit(ctx); err != nil {
			return err
		}
		return errors.New("commit acknowledgement lost")
	}
	_, err := service.createSnapshot(
		t.Context(), Owner{Scope: ScopeSystem}, "uncertain.txt", stringsReader("content"),
	)
	if err == nil {
		t.Fatal("createSnapshot did not surface the uncertain commit result")
	}
	var fileID string
	if err := database.QueryRow(t.Context(), `SELECT id FROM library_file`).Scan(&fileID); err != nil {
		t.Fatalf("committed metadata was lost: %v", err)
	}
	key, err := RawKey(fileID)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("potentially owned raw was deleted: %v", err)
	}
	_ = object.Close()
}

func TestPersonalQuotaSerializesUserAndUserAgent(t *testing.T) {
	database := dbtest.New(t)
	seedLibraryPrincipals(t, database)
	store, service := newLibraryService(t, database)
	if _, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeUser, UserID: testUserA},
		PersonalMaxBytes-1, "processing", nil,
	); err != nil {
		t.Fatal(err)
	}
	owners := []Owner{
		{Scope: ScopeUser, UserID: testUserA},
		{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA},
	}
	results := make([]error, len(owners))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index, owner := range owners {
		wait.Add(1)
		go func(index int, owner Owner) {
			defer wait.Done()
			<-start
			_, results[index] = service.createSnapshot(
				t.Context(), owner, "one-byte.txt", stringsReader("x"),
			)
		}(index, owner)
	}
	close(start)
	wait.Wait()
	var successes, quotaFailures int
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrQuotaExceeded):
			quotaFailures++
		default:
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if successes != 1 || quotaFailures != 1 {
		t.Fatalf("concurrent personal quota = %d successes, %d quota failures", successes, quotaFailures)
	}
	page, err := store.ListPage(t.Context(), RawPrefix, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 1 {
		t.Fatalf("raw object count = %d, want one committed object", len(page.Objects))
	}
}

func TestRawOwnershipQueryIncludesLiveAndTombstonedFiles(t *testing.T) {
	database := dbtest.New(t)
	liveID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Now().UTC()
	deletedID, err := insertLibraryFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", &deletedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := sqlc.New(database).GetLibraryRawOwners(t.Context(), []string{liveID, deletedID, uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("ownership rows = %d, want live and tombstoned rows", len(rows))
	}
	ownership := make(map[string]bool)
	for _, row := range rows {
		ownership[row.ID] = row.DeletedAt.Valid
	}
	if ownership[liveID] || !ownership[deletedID] {
		t.Fatalf("ownership tombstones = %+v", ownership)
	}
}

type countingReader struct {
	reader io.Reader
	reads  int
}

type blockingRawStore struct {
	RawStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingRawStore) Create(ctx context.Context, key string, reader io.Reader) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.RawStore.Create(ctx, key, reader)
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	r.reads++
	return r.reader.Read(buffer)
}

func stringsReader(value string) io.Reader { return bytes.NewBufferString(value) }

func newLibraryService(t *testing.T, database *pgxpool.Pool) (*FSRawStore, *Service) {
	t.Helper()
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service := newSnapshotServiceWithConfig(t, database, ServiceConfig{RawStore: store})
	return store, service
}

func newSnapshotServiceWithConfig(
	t *testing.T,
	database *pgxpool.Pool,
	config ServiceConfig,
) *Service {
	t.Helper()
	client, err := river.NewClient(riverpgxv5.New(database), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.DB = database
	config.River = client
	config.Parser = staticLibraryParser{}
	config.ParserProfile = testParserProfile
	config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	config.TempDir = t.TempDir()
	config.MaxConcurrentUploads = 4
	config.MaxSpoolBytes = 4 * MaxFileBytes
	service, err := NewService(config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type staticLibraryParser struct{}

func (staticLibraryParser) Parse(context.Context, string, string) ([]ParsedChunk, error) {
	return []ParsedChunk{{Content: "test library"}}, nil
}

func testAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(userID), admin)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func seedLibraryPrincipals(t *testing.T, database *pgxpool.Pool) {
	t.Helper()
	if _, err := database.Exec(t.Context(), `
		INSERT INTO auth_user (id, email)
		VALUES ($1, 'library-a@test.local'), ($2, 'library-b@test.local')
	`, testUserA, testUserB); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := sqlc.New(database).CreateAgent(t.Context(), sqlc.CreateAgentParams{
		ID: testAgentA, Name: testAgentA, Model: "test/model", Workspace: "/tmp/" + testAgentA,
		Sandbox: json.RawMessage(`{}`), EnabledBuiltinSkills: json.RawMessage(`[]`),
		Scope: string(config.AgentScopeSystem), Enabled: true,
	}); err != nil {
		t.Fatalf("seed Agent: %v", err)
	}
}

func insertLibraryFile(
	ctx context.Context,
	database *pgxpool.Pool,
	owner Owner,
	sizeBytes int64,
	status string,
	deletedAt *time.Time,
) (string, error) {
	id := uuid.NewString()
	var userID, agentID any
	if owner.UserID != "" {
		userID = owner.UserID
	}
	if owner.AgentID != "" {
		agentID = owner.AgentID
	}
	_, err := database.Exec(ctx, `
		INSERT INTO library_file (
			id, scope, user_id, agent_id, file_name, media_type,
			size_bytes, raw_sha256, status, deleted_at
		) VALUES ($1, $2, $3, $4, 'fixture.txt', 'text/plain', $5, $6, $7, $8)
	`, id, owner.Scope, userID, agentID, sizeBytes, bytes.Repeat([]byte{1}, sha256.Size), status, deletedAt)
	return id, err
}
