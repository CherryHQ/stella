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
		if result != "ok" || reply == "" {
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
	if !result.Valid || result.String != "ok" {
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
