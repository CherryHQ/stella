package library

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/runcontrol"
)

func TestStaleRunCannotCommitLibraryUpload(t *testing.T) {
	database := dbtest.New(t)
	store, service := newLibraryService(t, database)
	authority := testAuthority(t, testUserA, true)
	guard := terminalLibraryRunGuard(t, database)

	_, err := service.CreateManagedUpload(
		agentrun.WithGuard(t.Context(), guard), authority, ScopeSystem, "", "stale.txt", stringsReader("stale upload"),
	)
	if !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale upload error = %v, want %v", err, agentrun.ErrLeaseLost)
	}

	var files, jobs int
	if err := database.QueryRow(t.Context(), `SELECT count(*) FROM library_file`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(t.Context(), `
		SELECT count(*) FROM river_job WHERE kind = $1
	`, (chunkArgs{}).Kind()).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if files != 0 || jobs != 0 {
		t.Fatalf("stale upload committed files=%d jobs=%d, want no durable writes", files, jobs)
	}
	page, err := store.ListPage(t.Context(), RawPrefix, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("stale upload left %d raw objects", len(page.Objects))
	}
}

func TestStaleRunCannotCommitLibraryDelete(t *testing.T) {
	database := dbtest.New(t)
	_, service := newLibraryService(t, database)
	authority := testAuthority(t, testUserA, true)
	fileID, err := insertLibraryFile(t.Context(), database, Owner{Scope: ScopeSystem}, 1, "processing", nil)
	if err != nil {
		t.Fatal(err)
	}
	file, err := service.GetManaged(t.Context(), authority, fileID)
	if err != nil {
		t.Fatalf("GetManaged: %v", err)
	}

	err = service.DeleteManagedIfVersion(
		agentrun.WithGuard(t.Context(), terminalLibraryRunGuard(t, database)), authority, fileID,
		file.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale delete error = %v, want %v", err, agentrun.ErrLeaseLost)
	}

	var deletedAt *time.Time
	if err := database.QueryRow(t.Context(), `SELECT deleted_at FROM library_file WHERE id = $1`, fileID).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	var cleanupJobs int
	if err := database.QueryRow(t.Context(), `
		SELECT count(*) FROM river_job WHERE kind = $1 AND args ->> 'file_id' = $2
	`, (cleanupArgs{}).Kind(), fileID).Scan(&cleanupJobs); err != nil {
		t.Fatal(err)
	}
	if deletedAt != nil || cleanupJobs != 0 {
		t.Fatalf("stale delete committed deleted_at=%v cleanup_jobs=%d, want no durable writes", deletedAt, cleanupJobs)
	}
}

func terminalLibraryRunGuard(t *testing.T, database *pgxpool.Pool) agentrun.Guard {
	t.Helper()
	q := sqlc.New(database)
	bootID, runID := uuid.NewString(), uuid.NewString()
	sessionID := "library-run-fence-" + runID
	if _, err := q.CreateExecutorBoot(t.Context(), bootID); err != nil {
		t.Fatalf("create executor boot: %v", err)
	}
	if _, err := database.Exec(t.Context(), `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create run conversation: %v", err)
	}
	if _, err := q.CreateAgentRun(t.Context(), sqlc.CreateAgentRunParams{
		ID: runID, SessionID: sessionID, ExecutorBootID: bootID,
		Source: "library-run-fence", LeaseSeconds: 60,
	}); err != nil {
		t.Fatalf("create AgentRun: %v", err)
	}
	if _, err := q.CompleteAgentRunWithActivity(t.Context(), sqlc.CompleteAgentRunWithActivityParams{
		RunID: runID, ExecutorBootID: bootID,
		Status: agentrun.StatusCompleted, Reason: "library-run-fence",
		CompletionOutcome: string(runcontrol.OutcomeDelivered),
		TurnResult:        pgtype.Text{String: "success", Valid: true},
	}); err != nil {
		t.Fatalf("complete AgentRun: %v", err)
	}
	return agentrun.Guard{RunID: runID, SessionID: sessionID, ExecutorBootID: bootID}
}
