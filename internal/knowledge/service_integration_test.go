package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type staticParser struct {
	chunks []ParsedChunk
	err    error
}

func (p staticParser) Parse(context.Context, string, string) ([]ParsedChunk, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]ParsedChunk(nil), p.chunks...), nil
}

type unavailableParser struct {
	staticParser
}

func (unavailableParser) Available() error {
	return errors.New("managed parser is not installed")
}

type cancelAwareParser struct {
	started  chan struct{}
	canceled chan struct{}
}

func (p *cancelAwareParser) Parse(ctx context.Context, _, _ string) ([]ParsedChunk, error) {
	close(p.started)
	<-ctx.Done()
	close(p.canceled)
	return nil, ctx.Err()
}

func TestKnowledgeOwnerConstraint(t *testing.T) {
	db := dbtest.New(t)
	seedKnowledgePrincipals(t, db)

	valid := []Owner{
		{Scope: ScopeSystem},
		{Scope: ScopeSystemAgent, AgentID: testAgentA},
		{Scope: ScopeUser, UserID: testUserA},
		{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA},
	}
	for index, owner := range valid {
		if _, err := insertKnowledgeFile(t.Context(), db, owner, fmt.Sprintf("valid-%d.txt", index), "ready"); err != nil {
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
		{Scope: ScopeUserAgent, AgentID: testAgentA},
		{Scope: Scope("other")},
	}
	for index, owner := range invalid {
		if _, err := insertKnowledgeFile(t.Context(), db, owner, fmt.Sprintf("invalid-%d.txt", index), "ready"); err == nil {
			t.Fatalf("invalid owner %+v unexpectedly passed database CHECK", owner)
		}
	}
}

func TestKnowledgeServiceRiverReadySearchAndDelete(t *testing.T) {
	db := dbtest.New(t)
	service, client := newWorkingKnowledgeService(t, db, staticParser{
		chunks: []ParsedChunk{{
			Content: "The travel reimbursement ceiling is 800 yuan.",
			Locator: ChunkLocator{
				FirstPage:   uint32Pointer(2),
				LastPage:    uint32Pointer(2),
				HeadingPath: []string{"Finance", "Travel"},
				ByteStart:   10,
				ByteEnd:     60,
			},
		}},
	})
	startRiverClient(t, client)

	file, err := service.Create(t.Context(), Owner{Scope: ScopeSystem}, "travel.txt", []byte("source"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if file.Status != FileStatusProcessing {
		t.Fatalf("created status = %q, want processing", file.Status)
	}

	ready := waitForKnowledgeStatus(t, service, file.ID, FileStatusReady)
	if ready.ErrorMessage != "" {
		t.Fatalf("ready error message = %q", ready.ErrorMessage)
	}

	results, err := service.Search(t.Context(), testUserA, testAgentA, "travel reimbursement", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].FileName != "travel.txt" {
		t.Fatalf("Search results = %+v, want travel.txt", results)
	}
	if results[0].Locator == nil || results[0].Locator.FirstPage == nil || *results[0].Locator.FirstPage != 2 {
		t.Fatalf("public locator = %+v, want page 2", results[0].Locator)
	}
	if results[0].Locator.HeadingContext != "Finance > Travel" {
		t.Fatalf("heading context = %q, want Finance > Travel", results[0].Locator.HeadingContext)
	}

	if _, err := service.Delete(t.Context(), file.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var files, chunks int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_file`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_chunk`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if files != 0 || chunks != 0 {
		t.Fatalf("after delete: files=%d chunks=%d, want both zero", files, chunks)
	}
	results, err = service.Search(t.Context(), testUserA, testAgentA, "travel reimbursement", 5)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search after delete = %+v, want empty", results)
	}
}

func TestKnowledgeDeleteCancelsQueuedParseJob(t *testing.T) {
	db := dbtest.New(t)
	service := newInsertOnlyKnowledgeService(t, db)

	file, err := service.Create(t.Context(), Owner{Scope: ScopeSystem}, "queued.txt", []byte("source"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := service.Delete(t.Context(), file.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	state, found, err := service.latestParseJobState(t.Context(), file.ID)
	if err != nil {
		t.Fatalf("latest parse job: %v", err)
	}
	if !found || state != "cancelled" {
		t.Fatalf("parse job state = %q found=%v, want cancelled", state, found)
	}
}

func TestKnowledgeDeleteCancelsRunningParserAfterRead(t *testing.T) {
	db := dbtest.New(t)
	parser := &cancelAwareParser{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	service, client := newWorkingKnowledgeService(t, db, parser)
	startRiverClient(t, client)

	file, err := service.Create(t.Context(), Owner{Scope: ScopeSystem}, "running.txt", []byte("source"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-parser.started:
	case <-time.After(5 * time.Second):
		t.Fatal("parser did not start")
	}

	if _, err := service.Delete(t.Context(), file.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	select {
	case <-parser.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("running parser context was not cancelled after delete")
	}
	if _, err := service.Get(t.Context(), file.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete error = %v, want not found", err)
	}
}

func TestKnowledgeServiceRiverDeterministicFailure(t *testing.T) {
	db := dbtest.New(t)
	service, client := newWorkingKnowledgeService(t, db, staticParser{err: ErrNoExtractedText})
	startRiverClient(t, client)

	file, err := service.Create(t.Context(), Owner{Scope: ScopeSystem}, "empty.txt", []byte("source"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	failed := waitForKnowledgeStatus(t, service, file.ID, FileStatusFailed)
	if failed.ErrorMessage == "" {
		t.Fatal("failed file has no public error message")
	}

	var chunks int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_chunk WHERE file_id = $1`, file.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("failed file chunks = %d, want zero", chunks)
	}
}

func TestKnowledgeCreateRollsBackWhenRiverInsertFails(t *testing.T) {
	db := dbtest.New(t)
	service := newInsertOnlyKnowledgeService(t, db)

	// Renaming River's table makes InsertTx fail after knowledge_file has been
	// inserted, proving both writes are governed by the same transaction.
	if _, err := db.Exec(t.Context(), `ALTER TABLE river_job RENAME TO river_job_unavailable`); err != nil {
		t.Fatalf("rename river_job: %v", err)
	}
	_, err := service.Create(t.Context(), Owner{Scope: ScopeSystem}, "rollback.txt", []byte("source"))
	if err == nil {
		t.Fatal("Create unexpectedly succeeded without river_job")
	}

	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_file`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("knowledge_file count = %d, want zero after rollback", count)
	}
}

func TestKnowledgeCreateRejectsUnavailableParserBeforePersisting(t *testing.T) {
	db := dbtest.New(t)
	service := newKnowledgeService(t, db, unavailableParser{})
	client, err := river.NewClient(riverpgxv5.New(db), &river.Config{})
	if err != nil {
		t.Fatalf("new insert-only River client: %v", err)
	}
	service.SetRiverClient(client)

	_, err = service.Create(t.Context(), Owner{Scope: ScopeSystem}, "not-ready.txt", []byte("source"))
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("Create error = %v, want service unavailable", err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_file`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("knowledge_file count = %d, want zero while parser is unavailable", count)
	}
}

func TestPersonalQuotaSerializesUserAndUserAgent(t *testing.T) {
	db := dbtest.New(t)
	seedKnowledgePrincipals(t, db)
	service := newInsertOnlyKnowledgeService(t, db)

	// Seed one below the shared personal file limit. The two concurrent uploads
	// target different scopes but the same user and therefore the same lock.
	_, err := db.Exec(t.Context(), `
		INSERT INTO knowledge_file (
			scope, user_id, file_name, media_type, size_bytes, raw_content
		)
		SELECT
			'user', $1, 'seed-' || value || '.txt', 'text/plain', 1, convert_to('a', 'UTF8')
		FROM generate_series(1, $2::integer) AS value
	`, testUserA, PersonalMaxFiles-1)
	if err != nil {
		t.Fatalf("seed quota: %v", err)
	}

	owners := []Owner{
		{Scope: ScopeUser, UserID: testUserA},
		{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA},
	}
	errs := make(chan error, len(owners))
	var wait sync.WaitGroup
	for index, owner := range owners {
		wait.Add(1)
		go func(index int, owner Owner) {
			defer wait.Done()
			_, err := service.Create(context.Background(), owner, fmt.Sprintf("concurrent-%d.txt", index), []byte("b"))
			errs <- err
		}(index, owner)
	}
	wait.Wait()
	close(errs)

	var succeeded, quotaExceeded int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrQuotaExceeded):
			quotaExceeded++
		default:
			t.Fatalf("unexpected concurrent Create error: %v", err)
		}
	}
	if succeeded != 1 || quotaExceeded != 1 {
		t.Fatalf("concurrent results: succeeded=%d quota_exceeded=%d, want 1/1", succeeded, quotaExceeded)
	}

	quota, err := service.Quota(t.Context(), Owner{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA})
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if quota.UsedFiles != PersonalMaxFiles {
		t.Fatalf("personal used files = %d, want %d", quota.UsedFiles, PersonalMaxFiles)
	}
}

func TestKnowledgeSearchUsesExactlyFourVisibleScopes(t *testing.T) {
	db := dbtest.New(t)
	seedKnowledgePrincipals(t, db)
	service := newInsertOnlyKnowledgeService(t, db)

	visible := []struct {
		owner Owner
		name  string
	}{
		{Owner{Scope: ScopeSystem}, "system.txt"},
		{Owner{Scope: ScopeSystemAgent, AgentID: testAgentA}, "system-agent.txt"},
		{Owner{Scope: ScopeUser, UserID: testUserA}, "user.txt"},
		{Owner{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA}, "user-agent.txt"},
	}
	for _, fixture := range visible {
		fileID, err := insertKnowledgeFile(t.Context(), db, fixture.owner, fixture.name, "ready")
		if err != nil {
			t.Fatalf("insert %s: %v", fixture.name, err)
		}
		insertKnowledgeChunk(t, db, fileID, "orion handbook "+fixture.name)
	}

	hidden := []struct {
		owner Owner
		name  string
	}{
		{Owner{Scope: ScopeSystemAgent, AgentID: testAgentB}, "other-agent-system.txt"},
		{Owner{Scope: ScopeUser, UserID: testUserB}, "other-user.txt"},
		{Owner{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentB}, "same-user-other-agent.txt"},
		{Owner{Scope: ScopeUserAgent, UserID: testUserB, AgentID: testAgentA}, "other-user-same-agent.txt"},
	}
	for _, fixture := range hidden {
		fileID, err := insertKnowledgeFile(t.Context(), db, fixture.owner, fixture.name, "ready")
		if err != nil {
			t.Fatalf("insert %s: %v", fixture.name, err)
		}
		insertKnowledgeChunk(t, db, fileID, "orion handbook "+fixture.name)
	}

	processingID, err := insertKnowledgeFile(t.Context(), db, Owner{Scope: ScopeSystem}, "processing.txt", "processing")
	if err != nil {
		t.Fatal(err)
	}
	insertKnowledgeChunk(t, db, processingID, "orion handbook processing")

	results, err := service.Search(t.Context(), testUserA, testAgentA, "orion handbook", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != len(visible) {
		t.Fatalf("Search returned %d results: %+v; want exactly %d visible scopes", len(results), results, len(visible))
	}
	gotNames := make(map[string]bool, len(results))
	for _, result := range results {
		gotNames[result.FileName] = true
	}
	for _, fixture := range visible {
		if !gotNames[fixture.name] {
			t.Errorf("missing visible file %q from %+v", fixture.name, gotNames)
		}
	}
}

func TestKnowledgeSearchSupportsChineseAndMixedQueries(t *testing.T) {
	db := dbtest.New(t)
	seedKnowledgePrincipals(t, db)
	service := newInsertOnlyKnowledgeService(t, db)

	fileID, err := insertKnowledgeFile(
		t.Context(),
		db,
		Owner{Scope: ScopeSystem},
		"差旅制度.txt",
		"ready",
	)
	if err != nil {
		t.Fatal(err)
	}
	insertKnowledgeChunk(t, db, fileID, "Alpha-2026 差旅报销制度规定，住宿费用上限为八百元。")

	for _, query := range []string{"差旅 报销", "Alpha-2026 差旅"} {
		results, err := service.Search(t.Context(), testUserA, testAgentA, query, 5)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(results) != 1 || results[0].FileName != "差旅制度.txt" {
			t.Fatalf("Search(%q) = %+v, want Chinese policy file", query, results)
		}
	}
}

func TestKnowledgeSearchRejectsOversizedQuery(t *testing.T) {
	db := dbtest.New(t)
	service := newInsertOnlyKnowledgeService(t, db)

	_, err := service.Search(
		t.Context(),
		testUserA,
		testAgentA,
		strings.Repeat("知", MaxSearchQueryRunes+1),
		5,
	)
	if err == nil {
		t.Fatal("Search() accepted a query above MaxSearchQueryRunes")
	}
}

const (
	testUserA  = "10000000-0000-0000-0000-000000000001"
	testUserB  = "10000000-0000-0000-0000-000000000002"
	testAgentA = "knowledge-agent-a"
	testAgentB = "knowledge-agent-b"
)

func seedKnowledgePrincipals(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		INSERT INTO auth_user (id, email)
		VALUES ($1, 'knowledge-a@test.local'), ($2, 'knowledge-b@test.local')
	`, testUserA, testUserB); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	q := sqlc.New(db)
	for _, agentID := range []string{testAgentA, testAgentB} {
		if _, err := q.CreateAgent(t.Context(), sqlc.CreateAgentParams{
			ID: agentID, Name: agentID, Model: "test/model", Workspace: "/tmp/" + agentID,
			Sandbox: json.RawMessage(`{}`), EnabledBuiltinSkills: json.RawMessage(`[]`),
			Scope: "system", Enabled: true,
		}); err != nil {
			t.Fatalf("seed Agent %s: %v", agentID, err)
		}
	}
}

func newWorkingKnowledgeService(
	t *testing.T,
	db *pgxpool.Pool,
	parser Parser,
) (*Service, *river.Client[pgx.Tx]) {
	t.Helper()
	service := newKnowledgeService(t, db, parser)
	workers := river.NewWorkers()
	service.RegisterRiverWorkers(workers)
	queue, config := service.QueueConfig()
	client, err := appdb.NewWorkingRiverClient(
		db,
		map[string]river.QueueConfig{queue: config},
		workers,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		appdb.DefaultRiverSoftStopTimeout,
	)
	if err != nil {
		t.Fatalf("NewWorkingRiverClient: %v", err)
	}
	service.SetRiverClient(client)
	return service, client
}

func newInsertOnlyKnowledgeService(t *testing.T, db *pgxpool.Pool) *Service {
	t.Helper()
	service := newKnowledgeService(t, db, staticParser{})
	client, err := river.NewClient(riverpgxv5.New(db), &river.Config{})
	if err != nil {
		t.Fatalf("new insert-only River client: %v", err)
	}
	service.SetRiverClient(client)
	return service
}

func newKnowledgeService(t *testing.T, db *pgxpool.Pool, parser Parser) *Service {
	t.Helper()
	service, err := NewService(ServiceConfig{
		DB:      db,
		Parser:  parser,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func startRiverClient(t *testing.T, client *river.Client[pgx.Tx]) {
	t.Helper()
	if err := client.Start(t.Context()); err != nil {
		t.Fatalf("start River client: %v", err)
	}
	t.Cleanup(func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Stop(stopContext); err != nil {
			t.Errorf("stop River client: %v", err)
		}
	})
}

func waitForKnowledgeStatus(t *testing.T, service *Service, fileID string, want FileStatus) File {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		file, err := service.Get(t.Context(), fileID)
		if err != nil {
			t.Fatalf("Get while waiting for %q: %v", want, err)
		}
		if file.Status == want {
			return file
		}
		time.Sleep(20 * time.Millisecond)
	}
	file, err := service.Get(t.Context(), fileID)
	t.Fatalf("timed out waiting for %q; final file=%+v err=%v", want, file, err)
	return File{}
}

func insertKnowledgeFile(
	ctx context.Context,
	db *pgxpool.Pool,
	owner Owner,
	fileName string,
	status string,
) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO knowledge_file (
			scope, user_id, agent_id, file_name, media_type,
			size_bytes, raw_content, status, error_message
		)
		VALUES (
			$1, NULLIF($2, '')::uuid, NULLIF($3, ''), $4, 'text/plain',
			1, convert_to('a', 'UTF8'), $5,
			CASE WHEN $5 = 'failed' THEN 'failed for test' ELSE NULL END
		)
		RETURNING id
	`, owner.Scope, owner.UserID, owner.AgentID, fileName, status).Scan(&id)
	return id, err
}

func insertKnowledgeChunk(t *testing.T, db *pgxpool.Pool, fileID, content string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		INSERT INTO knowledge_chunk (file_id, ordinal, content, locator)
		VALUES ($1, 0, $2, '{}')
	`, fileID, content); err != nil {
		t.Fatalf("insert chunk for %s: %v", fileID, err)
	}
}
