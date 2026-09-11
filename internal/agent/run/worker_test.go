package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeExecutor stands in for the real turn driver: it records the adopted
// lease context and can block until released or the lease dies.
type fakeExecutor struct {
	reply  string
	err    error
	gate   chan struct{} // when non-nil, Execute waits for gate or ctx.Done
	called atomic.Int32
	sawRun atomic.Value
}

func (f *fakeExecutor) Execute(ctx context.Context, r sqlc.AgentRun) (string, error) {
	f.called.Add(1)
	f.sawRun.Store(r)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.reply, f.err
}

// replyHook mirrors the production finish hook: a successful turn appends the
// reply op inside the finish transaction.
func replyHook(t *testing.T) FinishHook {
	t.Helper()
	return func(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
		if result != "success" || reply == "" {
			return nil
		}
		var addr ReplyAddress
		if err := json.Unmarshal(r.ReplyAddress, &addr); err != nil {
			return err
		}
		ops, err := choutbox.ReplyOps(r.ID, choutbox.DeliveryKeyForRun(r.ID), addr.ChannelID, addr.AccountKey,
			choutbox.Address{V: choutbox.AddressVersion, ChatKey: addr.ChatKey, ThreadKey: addr.ThreadKey, ReplyToKey: addr.ReplyToKey}, reply, 0)
		if err != nil {
			return err
		}
		return choutbox.New(nil).Append(ctx, tx, ops)
	}
}

func enqueueForTest(t *testing.T, db *pgxpool.Pool, sessionID, key string) string {
	t.Helper()
	var id string
	err := lockSession(t, db, sessionID, func(tx pgx.Tx) error {
		row, _, err := New(db).Enqueue(t.Context(), tx, enqueueParams(sessionID, key))
		id = row.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWorkerClaimExecuteFinish(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	ctx := t.Context()

	var runID string
	err := lockSession(t, db, "sess-1", func(tx pgx.Tx) error {
		row, _, err := New(db).Enqueue(ctx, tx, enqueueParams("sess-1", "req-1"))
		runID = row.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{reply: "the answer"}
	w := NewWorker(db, "w1", exec, replyHook(t))
	ok, err := w.ProcessOnce(ctx)
	if err != nil || !ok {
		t.Fatalf("ProcessOnce: ok=%v err=%v", ok, err)
	}

	r, err := New(db).Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateCompleted) {
		t.Fatalf("run state = %s, want completed", r.State)
	}
	// Execution row deleted; session activity finished atomically.
	if _, err := sqlc.New(db).GetSessionExecution(ctx, "sess-1"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("execution row still present: %v", err)
	}
	var result pgtype.Text
	if err := db.QueryRow(ctx, "SELECT last_turn_result FROM ctx_conversation WHERE session_id='sess-1'").Scan(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.String != "success" {
		t.Fatalf("last_turn_result = %v", result)
	}
	// Reply op landed in the same transaction.
	var payload json.RawMessage
	if err := db.QueryRow(ctx, "SELECT payload FROM channel_outbox").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || body.Text != "the answer" {
		t.Fatalf("reply payload = %s err=%v", payload, err)
	}
	// Second sweep: nothing left.
	if ok, err := w.ProcessOnce(ctx); err != nil || ok {
		t.Fatalf("second ProcessOnce: ok=%v err=%v", ok, err)
	}
}

func TestWorkerSessionFenceOneClaimant(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	ctx := t.Context()
	enqueueID := enqueueForTest(t, db, "sess-1", "req-1")

	gate := make(chan struct{})
	execA := &fakeExecutor{reply: "a", gate: gate}
	wA := NewWorker(db, "worker-a", execA, replyHook(t))
	wB := NewWorker(db, "worker-b", &fakeExecutor{reply: "b"}, replyHook(t))

	doneA := make(chan bool, 1)
	go func() { ok, _ := wA.ProcessOnce(ctx); doneA <- ok }()
	// Worker B must not claim the same session while A holds the lease.
	time.Sleep(200 * time.Millisecond)
	okB, err := wB.ProcessOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if okB {
		t.Fatal("second worker claimed the same session's run")
	}
	close(gate)
	if !<-doneA {
		t.Fatal("worker A did not claim")
	}
	r, err := New(db).Get(ctx, enqueueID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateCompleted) || r.WorkerID.String != "worker-a" {
		t.Fatalf("run = %s/%s", r.State, r.WorkerID.String)
	}
}

func TestWorkerExpiredLeaseReapedNotReplayed(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	ctx := t.Context()
	runID := enqueueForTest(t, db, "sess-1", "req-1")

	gate := make(chan struct{})
	exec := &fakeExecutor{gate: gate}
	w := NewWorker(db, "w1", exec, replyHook(t))
	done := make(chan error, 1)
	go func() { _, err := w.ProcessOnce(ctx); done <- err }()

	// Wait until the worker actually claimed (execution row exists).
	var token string
	for range 100 {
		if err := db.QueryRow(ctx, "SELECT token FROM ctx_session_execution WHERE session_id='sess-1'").Scan(&token); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if token == "" {
		t.Fatal("lease never claimed")
	}

	// Kill the lease out from under the worker (as if the replica died).
	if _, err := db.Exec(ctx,
		"UPDATE ctx_session_execution SET lease_until = clock_timestamp() - interval '1s' WHERE session_id='sess-1'"); err != nil {
		t.Fatal(err)
	}
	n, err := New(db).ReapExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reap: n=%d err=%v", n, err)
	}
	r, err := New(db).Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateInterrupted) {
		t.Fatalf("run state = %s, want interrupted", r.State)
	}
	close(gate)
	select {
	case err := <-done:
		if err == nil {
			t.Log("worker finished after reaping — acceptable only if lease already gone")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not return after reap")
	}
	// The run is terminal; a fresh worker must not resurrect it.
	w2 := NewWorker(db, "w2", &fakeExecutor{reply: "replay"}, replyHook(t))
	if ok, err := w2.ProcessOnce(ctx); err != nil || ok {
		t.Fatalf("replay claimed: ok=%v err=%v", ok, err)
	}
}

func TestWorkerCancelRequestedCancelsRun(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	ctx := t.Context()
	runID := enqueueForTest(t, db, "sess-1", "req-1")

	gate := make(chan struct{})
	exec := &fakeExecutor{gate: gate}
	w := NewWorker(db, "w1", exec, replyHook(t))
	done := make(chan error, 1)
	go func() { _, err := w.ProcessOnce(ctx); done <- err }()

	var token string
	for range 100 {
		if err := db.QueryRow(ctx, "SELECT token FROM ctx_session_execution WHERE session_id='sess-1'").Scan(&token); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if token == "" {
		t.Fatal("lease never claimed")
	}
	if _, err := sqlc.New(db).CancelSessionExecution(ctx, sqlc.CancelSessionExecutionParams{SessionID: "sess-1", Token: token}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not return after cancel")
	}
	r, err := New(db).Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateCanceled) {
		t.Fatalf("run state = %s, want canceled", r.State)
	}
	if countOutboxOps(t, db) != 0 {
		t.Fatal("canceled run must not produce a reply op")
	}
}

// Drain decoupling: cancelling the loop ctx (stopIngress) must not cancel a
// turn the drain budget is still waiting on — the claimed turn runs under
// execCtx until it finishes or the final teardown cancels execCtx.
func TestWorkerDrainKeepsInflightTurn(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	runID := enqueueForTest(t, db, "sess-1", "req-1")

	loopCtx, stopClaims := context.WithCancel(context.Background())
	execCtx := t.Context()
	gate := make(chan struct{})
	exec := &fakeExecutor{reply: "drained reply", gate: gate}
	w := NewWorker(db, "w1", exec, replyHook(t), WithExecContext(execCtx))
	done := make(chan error, 1)
	go func() { _, err := w.ProcessOnce(loopCtx); done <- err }()

	for range 100 {
		if exec.called.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if exec.called.Load() == 0 {
		t.Fatal("executor never ran")
	}

	// Graceful drain begins: claiming stops, but the gated turn must survive.
	stopClaims()
	select {
	case <-done:
		t.Fatal("worker returned while the turn was still gated — the loop ctx cancelled the in-flight turn")
	case <-time.After(300 * time.Millisecond):
	}
	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not return after gate release")
	}
	r, err := New(db).Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateCompleted) {
		t.Fatalf("run state = %s, want completed — drain must not cancel the in-flight turn", r.State)
	}
	if countOutboxOps(t, db) != 1 {
		t.Fatal("drained worker's completed turn must still commit its reply op")
	}
}

// The execCtx bound is the teardown backstop: cancelling it mid-turn cancels
// the turn like any other lost-execution path.
func TestWorkerExecContextCancelBoundsTurn(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	runID := enqueueForTest(t, db, "sess-1", "req-1")

	execCtx, teardown := context.WithCancel(context.Background())
	exec := &fakeExecutor{gate: make(chan struct{})} // never released; only ctx ends it
	w := NewWorker(db, "w1", exec, replyHook(t), WithExecContext(execCtx))
	done := make(chan error, 1)
	go func() { _, err := w.ProcessOnce(t.Context()); done <- err }()

	for range 100 {
		if exec.called.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	teardown()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not return after teardown")
	}
	r, err := New(db).Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateCanceled) {
		t.Fatalf("run state = %s, want canceled", r.State)
	}
}

func countOutboxOps(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM channel_outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorkerTargetGoneMarksFailed(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	ctx := t.Context()
	runID := enqueueForTest(t, db, "sess-1", "req-1")

	// The executor's fresh authorize denies the dead target.
	exec := &fakeExecutor{err: fmt.Errorf("%w: session archived", ErrTargetGone)}
	w := NewWorker(db, "w1", exec, replyHook(t))
	if _, err := w.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := New(db).Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateFailed) || r.ErrorCode.String != ErrCodeTargetGone {
		t.Fatalf("run = %s/%s", r.State, r.ErrorCode.String)
	}
}

// An archived target alone in the queue must reach a terminal state in its own
// committed transaction — and take no lease on the way.
func TestWorkerArchivedOnlyRunFailsAndCommits(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "archived", "agent-1")
	runID := enqueueForTest(t, db, "archived", "only")
	if _, err := db.Exec(t.Context(), "UPDATE ctx_conversation SET archived=true WHERE session_id='archived'"); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, "w1", &fakeExecutor{reply: "reply"}, replyHook(t))
	claimed, err := w.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("archived target must not be claimed")
	}
	r, err := New(db).Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(StateFailed) || r.ErrorCode.String != ErrCodeTargetGone {
		t.Fatalf("run = %s/%s, want failed/target_gone", r.State, r.ErrorCode.String)
	}
	if _, err := sqlc.New(db).GetSessionExecution(t.Context(), "archived"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("skipped candidate must not leave an execution lease, err=%v", err)
	}
}

// A candidate skipped by the FIFO head check must not leave a committed lease
// when a later healthy candidate in the same sweep commits.
func TestWorkerSkippedFIFOCandidateLeavesNoLease(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "fifo", "agent-1")
	createSession(t, db, "healthy", "agent-1")
	first := enqueueForTest(t, db, "fifo", "first")
	second := enqueueForTest(t, db, "fifo", "second")
	enqueueForTest(t, db, "healthy", "healthy")

	// Lock the head so the scan still lists it but the successor is skipped by
	// the earlier-queued check rather than leapfrogging.
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), "SELECT id FROM agent_run WHERE id=$1 FOR UPDATE", first); err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{reply: "reply"}
	w := NewWorker(db, "w1", exec, replyHook(t))
	claimed, err := w.ProcessOnce(t.Context())
	if err != nil || !claimed {
		t.Fatalf("healthy successor claim: claimed=%v err=%v", claimed, err)
	}
	r2, err := New(db).Get(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if r2.State != string(StateQueued) {
		t.Fatalf("skipped FIFO run state=%q, want queued", r2.State)
	}
	if _, err := sqlc.New(db).GetSessionExecution(t.Context(), "fifo"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("skipped candidate left an unadopted lease, err=%v", err)
	}
}

// The finished activity must use the success/error vocabulary the session API
// understands — a "ok" result renders as unexplained idle.
func TestWorkerSuccessWritesActivitySuccess(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	enqueueForTest(t, db, "sess-1", "req-1")
	w := NewWorker(db, "w1", &fakeExecutor{reply: "done"}, replyHook(t))
	if ok, err := w.ProcessOnce(t.Context()); err != nil || !ok {
		t.Fatalf("claimed=%v err=%v", ok, err)
	}
	var result string
	if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id='sess-1'").Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "success" {
		t.Fatalf("last_turn_result=%q, want success", result)
	}
}

// D9 end-to-end at the worker seam: while the previous writer's process is
// verifiably alive, a second worker must not claim the session's queued run —
// the session's writable environment stays unavailable. Once the fenced-out
// writer finishes unwinding and clears its expired row, the queued run
// proceeds and completes exactly once.
func TestWorkerTakeoverWaitsForWriterExit(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	ctx := t.Context()
	enqueueForTest(t, db, "sess-1", "req-1")

	gate := make(chan struct{})
	exec1 := &fakeExecutor{gate: gate}
	w1 := NewWorker(db, "w1", exec1, replyHook(t))
	done := make(chan error, 1)
	go func() { _, err := w1.ProcessOnce(ctx); done <- err }()

	for range 100 {
		if exec1.called.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if exec1.called.Load() == 0 {
		t.Fatal("w1 never started the turn")
	}

	// The lease lapses while w1's process is still alive (partitioned, not
	// dead): expire it, enqueue the next turn, and another worker must be
	// fenced out — the run stays queued rather than dual-writing.
	if _, err := db.Exec(ctx, "UPDATE ctx_session_execution SET lease_until = clock_timestamp() - interval '1 second' WHERE session_id='sess-1'"); err != nil {
		t.Fatal(err)
	}
	run2 := enqueueForTest(t, db, "sess-1", "req-2")
	w2 := NewWorker(db, "w2", &fakeExecutor{reply: "second"}, replyHook(t))
	if ok, err := w2.ProcessOnce(ctx); err != nil || ok {
		t.Fatalf("w2 claimed while writer alive: ok=%v err=%v", ok, err)
	}
	var state2 string
	if err := db.QueryRow(ctx, "SELECT state FROM agent_run WHERE id=$1", run2).Scan(&state2); err != nil {
		t.Fatal(err)
	}
	if state2 != string(StateQueued) {
		t.Fatalf("run2 state = %s, want queued (resource unavailable)", state2)
	}

	// w1's turn ends: its lease was fenced, so Finish reports ErrLost and
	// clears its own expired row — the exit attest.
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("w1 ProcessOnce returned nil despite a fenced lease")
	}

	// Now the writer is proven gone: w2 claims and completes run2 once.
	if ok, err := w2.ProcessOnce(ctx); err != nil || !ok {
		t.Fatalf("w2 could not claim after writer exit: ok=%v err=%v", ok, err)
	}
	r2, err := New(db).Get(ctx, run2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.State != string(StateCompleted) {
		t.Fatalf("run2 state = %s, want completed", r2.State)
	}
}
