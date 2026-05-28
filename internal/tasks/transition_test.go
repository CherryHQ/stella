package tasks

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// testHarness wires a fresh SQLite + seeded org/user + TransitionService.
type testHarness struct {
	t      *testing.T
	db     *sql.DB
	q      *sqlc.Queries
	svc    *TransitionService
	orgID  string
	userID string
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	orgID, err := appdb.EnsureDefaultOrg(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultOrg: %v", err)
	}
	userID := uuid.NewString()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO auth_user (id, email) VALUES (?, ?)`,
		userID, "test-"+userID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	q := sqlc.New(db)
	svc := NewTransitionService(db, q)
	return &testHarness{t: t, db: db, q: q, svc: svc, orgID: orgID, userID: userID}
}

// createTask inserts a task in the given status.
func (h *testHarness) createTask(t *testing.T, status string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().Format(time.RFC3339Nano)
	if _, err := h.q.CreateAgentTask(context.Background(), sqlc.CreateAgentTaskParams{
		ID: id, OrgID: h.orgID, UserID: h.userID,
		Title: "t-" + id[:8], Status: status, Priority: "routine",
		Required: 1, RetryCount: 0, MaxRetries: 3,
		Context: "{}", Output: "{}",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return id
}

func (h *testHarness) getTask(t *testing.T, id string) sqlc.AgentTask {
	t.Helper()
	task, err := h.q.GetAgentTask(context.Background(), id)
	if err != nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	return task
}

// ---------------------------------------------------------------------------
// Transition matrix — one test per cell in D2's task transition table.
// ---------------------------------------------------------------------------

func TestActivate_DraftToReady(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusDraft)
	if err := h.svc.Activate(context.Background(), id, SystemActor()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready", got)
	}
}

func TestActivate_FromReady_ReturnsInvalidTransition(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	err := h.svc.Activate(context.Background(), id, SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestClaim_ReadyToRunning_InsertsRun_SetsActiveRun(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: id, ExecutorAgentID: "", NewSessionID: "sess-1",
		WorkerID: "w-1", LeaseDuration: 30 * time.Second,
		Actor: SystemActor(),
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.RunID == "" || res.SessionID != "sess-1" {
		t.Fatalf("ClaimResult: %+v", res)
	}
	task := h.getTask(t, id)
	if task.Status != StatusRunning {
		t.Errorf("status=%q want running", task.Status)
	}
	if !task.ActiveRunID.Valid || task.ActiveRunID.String != res.RunID {
		t.Errorf("active_run_id=%v want %s", task.ActiveRunID, res.RunID)
	}
	if !task.SessionID.Valid || task.SessionID.String != "sess-1" {
		t.Errorf("session_id=%v want sess-1 (persisted on first run)", task.SessionID)
	}
}

func TestClaim_SecondClaimReusesSession(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if _, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: id, NewSessionID: "sess-1",
	}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Force back to ready via Fail-retry. The dispatcher always passes the
	// active run id so the run row is finalized; the test mirrors that.
	first := h.getTask(t, id)
	if err := h.svc.Fail(context.Background(), FailParams{
		TaskID: id, RunID: first.ActiveRunID.String, Reason: "test", Retryable: true,
	}); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	res, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: id, NewSessionID: "sess-2", // ignored because task already has session
	})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if res.SessionID != "sess-1" {
		t.Errorf("retry should reuse session, got %q", res.SessionID)
	}
}

func TestClaim_FromDraft_Rejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusDraft)
	_, err := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestSubmit_RunningToDone_FinalizesRun_ClearsActiveRun(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if err := h.svc.Submit(context.Background(), id, res.RunID, `{"ok":true}`, SystemActor()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusDone {
		t.Errorf("status=%q want done", task.Status)
	}
	if task.ActiveRunID.Valid {
		t.Errorf("active_run_id should be cleared after submit, got %v", task.ActiveRunID)
	}
	if !task.CompletedAt.Valid {
		t.Errorf("completed_at should be set")
	}
	if task.Output != `{"ok":true}` {
		t.Errorf("output=%q want {\"ok\":true}", task.Output)
	}
}

func TestBlock_RunningToBlocked_CreatesBlocker(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	err := h.svc.Block(context.Background(), BlockParams{
		TaskID: id, Kind: BlockerKindUserInput, Question: "need approval",
		RunID: res.RunID, Actor: SystemActor(),
	})
	if err != nil {
		t.Fatalf("Block: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusBlocked {
		t.Errorf("status=%q want blocked", task.Status)
	}
	if !task.ActiveBlockerID.Valid {
		t.Fatalf("active_blocker_id should be set")
	}
	open, err := h.q.GetOpenBlockerForTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOpenBlockerForTask: %v", err)
	}
	if open.Kind != BlockerKindUserInput || open.Question != "need approval" {
		t.Errorf("blocker mismatch: %+v", open)
	}
}

func TestBlock_SecondBlockMergesIntoExistingDetail(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if err := h.svc.Block(context.Background(), BlockParams{
		TaskID: id, Kind: BlockerKindUserInput, Question: "Q1", RunID: res.RunID,
	}); err != nil {
		t.Fatalf("first block: %v", err)
	}
	// Second block while still open should merge, not insert.
	if err := h.svc.Block(context.Background(), BlockParams{
		TaskID: id, Kind: BlockerKindToolError, Question: "Q2",
	}); err != nil {
		t.Fatalf("second block: %v", err)
	}
	rows, err := h.q.ListAgentTaskBlockersByTask(context.Background(), id)
	if err != nil {
		t.Fatalf("ListAgentTaskBlockersByTask: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly one blocker row, got %d", len(rows))
	}
	if rows[0].Detail == "" || rows[0].Detail == "{}" {
		t.Errorf("expected detail to have merged entries, got %q", rows[0].Detail)
	}
}

func TestResolveBlocker_BlockedToReady(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	_ = h.svc.Block(context.Background(), BlockParams{TaskID: id, Kind: BlockerKindUserInput, RunID: res.RunID})
	bl, _ := h.q.GetOpenBlockerForTask(context.Background(), id)
	if err := h.svc.ResolveBlocker(context.Background(), bl.ID, `{"answer":"ok"}`, SystemActor()); err != nil {
		t.Fatalf("ResolveBlocker: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusReady {
		t.Errorf("status=%q want ready", task.Status)
	}
	if task.ActiveBlockerID.Valid {
		t.Errorf("active_blocker_id should be cleared, got %v", task.ActiveBlockerID)
	}
}

func TestResolveBlocker_DepFailureRejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if err := h.svc.Block(context.Background(), BlockParams{
		TaskID: id, Kind: BlockerKindDepFailure, Question: "upstream failed",
	}); err != nil {
		t.Fatalf("Block: %v", err)
	}
	bl, _ := h.q.GetOpenBlockerForTask(context.Background(), id)
	err := h.svc.ResolveBlocker(context.Background(), bl.ID, "{}", SystemActor())
	if !errors.Is(err, ErrDepFailureUnresolved) {
		t.Fatalf("want ErrDepFailureUnresolved, got %v", err)
	}
}

func TestWaiveDep_UnblocksTask(t *testing.T) {
	h := newHarness(t)
	upstream := h.createTask(t, StatusFailed)
	downstream := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), downstream, upstream, DepKindHard, OnFailureBlock); err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	// Simulate the dispatcher propagating a dep_failure.
	if err := h.svc.Block(context.Background(), BlockParams{
		TaskID: downstream, Kind: BlockerKindDepFailure, Question: "upstream failed",
	}); err != nil {
		t.Fatalf("Block(dep_failure): %v", err)
	}
	// WaiveDep should both record waiver and resolve the blocker.
	if err := h.svc.WaiveDep(context.Background(), downstream, upstream, h.userID, "manual override", SystemActor()); err != nil {
		t.Fatalf("WaiveDep: %v", err)
	}
	task := h.getTask(t, downstream)
	if task.Status != StatusReady {
		t.Errorf("status=%q want ready after waiver", task.Status)
	}
	deps, _ := h.q.ListAgentTaskDeps(context.Background(), downstream)
	if len(deps) != 1 || !deps[0].WaivedAt.Valid {
		t.Errorf("dep edge should be waived, got %+v", deps)
	}
}

func TestFail_RetryableReturnsToReady_IncrementsRetry(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if err := h.svc.Fail(context.Background(), FailParams{
		TaskID: id, RunID: res.RunID, Reason: "transient", Retryable: true,
	}); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusReady {
		t.Errorf("status=%q want ready", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry_count=%d want 1", task.RetryCount)
	}
}

func TestFail_BudgetExhausted_GoesTerminal(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	// Force retry budget low: set max_retries=1 via direct UPDATE (test plumbing).
	if _, err := h.db.Exec(`UPDATE agent_task SET max_retries = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("set max_retries: %v", err)
	}
	// First fail: should retry.
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if err := h.svc.Fail(context.Background(), FailParams{TaskID: id, RunID: res.RunID, Retryable: true}); err != nil {
		t.Fatalf("first fail: %v", err)
	}
	// Second fail: budget exhausted → failed.
	res, _ = h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if err := h.svc.Fail(context.Background(), FailParams{TaskID: id, RunID: res.RunID, Retryable: true}); err != nil {
		t.Fatalf("second fail: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusFailed {
		t.Errorf("status=%q want failed", got)
	}
}

func TestCancel_FromAnyNonTerminal_Works(t *testing.T) {
	for _, start := range []string{StatusDraft, StatusReady} {
		t.Run(start, func(t *testing.T) {
			h := newHarness(t)
			id := h.createTask(t, start)
			if err := h.svc.Cancel(context.Background(), id, "user", SystemActor()); err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			task := h.getTask(t, id)
			if task.Status != StatusCancelled || !task.CancelledAt.Valid {
				t.Errorf("expected cancelled status + timestamp, got %+v", task)
			}
		})
	}
}

func TestCancel_FromRunning_CancelsActiveRun(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	if err := h.svc.Cancel(context.Background(), id, "user cancelled", SystemActor()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusCancelled {
		t.Errorf("status=%q want cancelled", task.Status)
	}
	if task.ActiveRunID.Valid {
		t.Errorf("active_run_id should be cleared")
	}
	run, _ := h.q.GetAgentTaskRun(context.Background(), res.RunID)
	if run.Status != RunCancelled {
		t.Errorf("run status=%q want cancelled", run.Status)
	}
}

func TestCancel_FromBlocked_CancelsBlocker(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if err := h.svc.Block(context.Background(), BlockParams{TaskID: id, Kind: BlockerKindUserInput}); err != nil {
		t.Fatalf("Block: %v", err)
	}
	bl, _ := h.q.GetOpenBlockerForTask(context.Background(), id)
	if err := h.svc.Cancel(context.Background(), id, "drop it", SystemActor()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusCancelled || task.ActiveBlockerID.Valid {
		t.Errorf("expected cancelled + no active_blocker_id, got %+v", task)
	}
	after, _ := h.q.GetAgentTaskBlocker(context.Background(), bl.ID)
	if after.Status != BlockerCancelled {
		t.Errorf("blocker status=%q want cancelled", after.Status)
	}
}

func TestCancel_TaskNotFound(t *testing.T) {
	h := newHarness(t)
	err := h.svc.Cancel(context.Background(), "no-such-task", "", SystemActor())
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("want ErrTaskNotFound, got %v", err)
	}
}

func TestSubmit_FromReady_Rejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	err := h.svc.Submit(context.Background(), id, "", "{}", SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestWaiveDep_NoMatchingEdge_ReturnsError(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	err := h.svc.WaiveDep(context.Background(), a, "no-such-dep", h.userID, "x", SystemActor())
	if err == nil {
		t.Fatal("expected error for missing dep")
	}
}

func TestWaiveDep_EmptyReason_Rejected(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), a, b, DepKindHard, OnFailureBlock)
	err := h.svc.WaiveDep(context.Background(), a, b, h.userID, "", SystemActor())
	if err == nil {
		t.Fatal("expected error for empty reason")
	}
}

func TestResolveBlocker_NotFound(t *testing.T) {
	h := newHarness(t)
	err := h.svc.ResolveBlocker(context.Background(), "no-such-blocker", "{}", SystemActor())
	if !errors.Is(err, ErrBlockerNotFound) {
		t.Fatalf("want ErrBlockerNotFound, got %v", err)
	}
}

func TestCancel_FromTerminal_Rejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"})
	_ = h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor())
	// Now done → cancel rejected.
	err := h.svc.Cancel(context.Background(), id, "after-the-fact", SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestCancel_Idempotent(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if err := h.svc.Cancel(context.Background(), id, "x", SystemActor()); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if err := h.svc.Cancel(context.Background(), id, "x again", SystemActor()); err != nil {
		t.Fatalf("second cancel should be idempotent, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DAG: cycle detection + dep CRUD.
// ---------------------------------------------------------------------------

func TestAddDep_AcyclicAccepted(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock); err != nil {
		t.Fatalf("AddDep b->a: %v", err)
	}
}

func TestAddDep_SelfLoopRejected(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	err := h.svc.AddDep(context.Background(), a, a, DepKindHard, OnFailureBlock)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("want ErrCycle, got %v", err)
	}
}

func TestAddDep_TwoNodeCycleRejected(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock); err != nil {
		t.Fatalf("AddDep b->a: %v", err)
	}
	err := h.svc.AddDep(context.Background(), a, b, DepKindHard, OnFailureBlock)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("want ErrCycle, got %v", err)
	}
}

func TestAddDep_ThreeNodeTransitiveCycleRejected(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	c := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.AddDep(context.Background(), c, b, DepKindHard, OnFailureBlock); err != nil {
		t.Fatal(err)
	}
	err := h.svc.AddDep(context.Background(), a, c, DepKindHard, OnFailureBlock)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("want ErrCycle, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Invariants enforced across paths.
// ---------------------------------------------------------------------------

func TestInvariant_RunningHasActiveRun(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if _, err := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	task := h.getTask(t, id)
	if !task.ActiveRunID.Valid {
		t.Errorf("invariant: running task must have active_run_id, got %v", task.ActiveRunID)
	}
}

func TestInvariant_BlockedHasOpenBlocker(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if err := h.svc.Block(context.Background(), BlockParams{TaskID: id, Kind: BlockerKindUserInput}); err != nil {
		t.Fatalf("Block: %v", err)
	}
	task := h.getTask(t, id)
	if !task.ActiveBlockerID.Valid {
		t.Errorf("invariant: blocked task must have active_blocker_id, got %v", task.ActiveBlockerID)
	}
	open, err := h.q.GetOpenBlockerForTask(context.Background(), id)
	if err != nil {
		t.Errorf("expected open blocker to exist: %v", err)
	}
	if open.Status != BlockerOpen {
		t.Errorf("blocker status=%q want open", open.Status)
	}
}

func TestInvariant_AtMostOneActiveWorkerRun(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if _, err := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s"}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Second claim should fail — task is running.
	_, err := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, NewSessionID: "s2"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition on double claim, got %v", err)
	}
	// And the uniqueness index must reject any raw INSERT too.
	_, err = h.db.Exec(`INSERT INTO agent_task_run
		(id, task_id, org_id, user_id, kind, attempt_no, status, session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'worker', 99, 'running', 's', ?, ?)`,
		uuid.NewString(), id, h.orgID, h.userID, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))
	if err == nil {
		t.Errorf("uniq_active_worker_run should reject second active run")
	}
}

func TestInvariant_AtMostOneOpenBlocker(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	// First open blocker via the service.
	if err := h.svc.Block(context.Background(), BlockParams{TaskID: id, Kind: BlockerKindUserInput}); err != nil {
		t.Fatalf("Block: %v", err)
	}
	// Raw INSERT of a second open blocker must violate the partial unique index.
	_, err := h.db.Exec(`INSERT INTO agent_task_blocker (id, task_id, kind, status, created_at) VALUES (?, ?, 'user_input', 'open', ?)`,
		uuid.NewString(), id, time.Now().Format(time.RFC3339))
	if err == nil {
		t.Errorf("uniq_open_blocker_per_task should reject second open row")
	}
}

func TestInvariant_StatusWriteRaceLoses(t *testing.T) {
	// Simulates two transitions racing the same row. The second one's
	// conditional UPDATE sees status != from and returns ErrInvalidTransition.
	h := newHarness(t)
	id := h.createTask(t, StatusDraft)
	if err := h.svc.Activate(context.Background(), id, SystemActor()); err != nil {
		t.Fatalf("first activate: %v", err)
	}
	if err := h.svc.Activate(context.Background(), id, SystemActor()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second activate: want ErrInvalidTransition, got %v", err)
	}
}
