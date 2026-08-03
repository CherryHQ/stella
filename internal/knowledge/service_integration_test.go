package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	testUserA  = "10000000-0000-0000-0000-000000000001"
	testUserB  = "10000000-0000-0000-0000-000000000002"
	testAgentA = "knowledge-agent-a"
)

func TestKnowledgeSchemaEnforcesOwnerAndActivePointer(t *testing.T) {
	database := dbtest.New(t)
	seedKnowledgePrincipals(t, database)
	valid := []Owner{
		{Scope: ScopeSystem},
		{Scope: ScopeSystemAgent, AgentID: testAgentA},
		{Scope: ScopeUser, UserID: testUserA},
		{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA},
	}
	for index, owner := range valid {
		if _, err := insertKnowledgeFile(t.Context(), database, owner, int64(index), "processing", nil); err != nil {
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
		if _, err := insertKnowledgeFile(t.Context(), database, owner, 1, "processing", nil); err == nil {
			t.Fatalf("invalid owner %+v passed database CHECK", owner)
		}
	}
	if _, err := insertKnowledgeFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, -1, "processing", nil,
	); err == nil {
		t.Fatal("negative size passed database CHECK")
	}
	// Status is deliberately a Go-validated evolvable value, not a database
	// enum; adding a lifecycle state should not require dropping a CHECK first.
	if _, err := insertKnowledgeFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "future_status", nil,
	); err != nil {
		t.Fatalf("evolvable status was rejected: %v", err)
	}

	fileA, err := insertKnowledgeFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fileB, err := insertKnowledgeFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	setA := uuid.NewString()
	setB := uuid.NewString()
	for _, value := range []struct{ setID, fileID string }{{setA, fileA}, {setB, fileB}} {
		if _, err := database.Exec(t.Context(), `
			INSERT INTO knowledge_chunk_set
				(id, file_id, derivation_key, processor_key, raw_sha256)
			VALUES ($1, $2, $3, 'xberg:test', $4)
		`, value.setID, value.fileID, "derivation:"+value.setID, bytes.Repeat([]byte{1}, sha256.Size)); err != nil {
			t.Fatalf("insert ChunkSet: %v", err)
		}
	}
	if _, err := database.Exec(
		t.Context(), `UPDATE knowledge_file SET active_chunk_set_id = $1 WHERE id = $2`, setB, fileA,
	); err == nil {
		t.Fatal("active pointer accepted another file's ChunkSet")
	}
	if _, err := database.Exec(
		t.Context(), `UPDATE knowledge_file SET active_chunk_set_id = $1 WHERE id = $2`, setA, fileA,
	); err != nil {
		t.Fatalf("same-file active pointer was rejected: %v", err)
	}
	if _, err := database.Exec(t.Context(), `DELETE FROM knowledge_file WHERE id = $1`, fileA); err != nil {
		t.Fatalf("file deletion did not cascade through its active ChunkSet: %v", err)
	}
}

func TestCreateManagedUploadCommitsRawMetadataAndUniqueJob(t *testing.T) {
	database := dbtest.New(t)
	store, service := newKnowledgeService(t, database)
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
		WHERE kind = 'stella_knowledge_chunk'
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
		WHERE table_name = 'knowledge_file' AND column_name = 'raw_content'
	`).Scan(&rawColumnCount); err != nil {
		t.Fatal(err)
	}
	if rawColumnCount != 0 {
		t.Fatal("raw_content BYTEA still exists in knowledge_file")
	}
}

func TestCreateManagedUploadAuthorizesBeforeReadingBody(t *testing.T) {
	database := dbtest.New(t)
	_, service := newKnowledgeService(t, database)
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
		DB: database, RawStore: blocking, River: client,
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

func TestCreateSnapshotCompensatesKnownPreCommitFailure(t *testing.T) {
	database := dbtest.New(t)
	store, service := newKnowledgeService(t, database)
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
	if err := database.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_file`).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 0 {
		t.Fatalf("knowledge_file count = %d after transaction rollback", fileCount)
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
	store, service := newKnowledgeService(t, database)
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
	if err := database.QueryRow(t.Context(), `SELECT id FROM knowledge_file`).Scan(&fileID); err != nil {
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
	seedKnowledgePrincipals(t, database)
	store, service := newKnowledgeService(t, database)
	if _, err := insertKnowledgeFile(
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
	liveID, err := insertKnowledgeFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Now().UTC()
	deletedID, err := insertKnowledgeFile(
		t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", &deletedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := sqlc.New(database).GetKnowledgeRawOwners(t.Context(), []string{liveID, deletedID, uuid.NewString()})
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

func newKnowledgeService(t *testing.T, database *pgxpool.Pool) (*FSRawStore, *Service) {
	t.Helper()
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(database), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		DB: database, RawStore: store, River: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TempDir: t.TempDir(),
		MaxConcurrentUploads: 4, MaxSpoolBytes: 4 * MaxFileBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, service
}

func testAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(userID), admin)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func seedKnowledgePrincipals(t *testing.T, database *pgxpool.Pool) {
	t.Helper()
	if _, err := database.Exec(t.Context(), `
		INSERT INTO auth_user (id, email)
		VALUES ($1, 'knowledge-a@test.local'), ($2, 'knowledge-b@test.local')
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

func insertKnowledgeFile(
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
		INSERT INTO knowledge_file (
			id, scope, user_id, agent_id, file_name, media_type,
			size_bytes, raw_sha256, status, deleted_at
		) VALUES ($1, $2, $3, $4, 'fixture.txt', 'text/plain', $5, $6, $7, $8)
	`, id, owner.Scope, userID, agentID, sizeBytes, bytes.Repeat([]byte{1}, sha256.Size), status, deletedAt)
	return id, err
}
