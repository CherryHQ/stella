package agentrun_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestLeaseHeartbeatOutlivesCanceledTurn(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	store := agentrun.NewStoreWithLease(ctx, db, bootID, 600*time.Millisecond)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "test")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Runtime cancellation belongs to the model turn. It must not cancel the
	// Store-owned lease while an adapter is still delivering the final event.
	turnCtx, cancelTurn := context.WithCancel(lease.Context())
	cancelTurn()
	if err := turnCtx.Err(); err == nil {
		t.Fatal("canceled turn context has nil error")
	}
	if err := lease.Context().Err(); err != nil {
		t.Fatalf("lease context canceled with turn: %v", err)
	}

	// The 800ms tail crosses at least one 200ms heartbeat tick and the initial
	// one-second database lease. The ownership fence proves the lease kept
	// renewing: an expired Run would fail it.
	time.Sleep(800 * time.Millisecond)
	if err := agentrun.Check(lease.ContextWith(ctx)); err != nil {
		t.Fatalf("ownership lost during slow tail: %v", err)
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("finish after slow tail: %v", err)
	}
}

func TestAcquireForInboxRebindsTargetGuardAtomically(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sourceSessionID := uuid.NewString()
	targetSessionID := uuid.NewString()
	bootID := uuid.NewString()
	for _, sessionID := range []string{sourceSessionID, targetSessionID} {
		if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
			t.Fatalf("create conversation %s: %v", sessionID, err)
		}
	}
	q := sqlc.New(db)
	store := agentrun.NewStoreWithLease(ctx, db, bootID, time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	source, err := store.Acquire(ctx, sourceSessionID, "chat")
	if err != nil {
		t.Fatalf("acquire source: %v", err)
	}
	inbox, err := q.EnqueueSessionInbox(ctx, sqlc.EnqueueSessionInboxParams{
		ID: uuid.NewString(), SourceSessionID: sourceSessionID, TargetSessionID: targetSessionID,
		ActorID: "agent", Content: "hello",
	})
	if err != nil {
		t.Fatalf("enqueue inbox: %v", err)
	}
	target, err := store.AcquireForInbox(source.ContextWith(ctx), targetSessionID, "inbox", inbox.ID, func(ctx context.Context, tx pgx.Tx, guard agentrun.Guard) error {
		return nil
	})
	if err != nil {
		t.Fatalf("acquire target for inbox: %v", err)
	}
	if target.Guard.SessionID != targetSessionID || target.Guard.RunID == source.Guard.RunID {
		t.Fatalf("target guard = %#v, source = %#v", target.Guard, source.Guard)
	}
	linked, err := q.GetSessionInbox(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("read linked inbox: %v", err)
	}
	if !linked.RunID.Valid || linked.RunID.String != target.Guard.RunID {
		t.Fatalf("inbox run_id = %#v, want %s", linked.RunID, target.Guard.RunID)
	}
	if err := source.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("finish source: %v", err)
	}
	if err := target.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("finish target: %v", err)
	}
}

// A caller-chosen status is exactly what the Run records, and Session activity
// derives from that same statement, so the two can never disagree.
func TestTerminalStatusDrivesSessionActivity(t *testing.T) {
	for _, tc := range []struct {
		status   string
		activity string
	}{
		{agentrun.StatusCompleted, "success"},
		{agentrun.StatusFailed, "error"},
		{agentrun.StatusCanceled, "canceled"},
		{agentrun.StatusInterrupted, "error"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			db := dbtest.New(t)
			ctx := t.Context()
			sessionID := uuid.NewString()
			if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
				t.Fatalf("create conversation: %v", err)
			}
			store := agentrun.NewStoreWithLease(ctx, db, uuid.NewString(), time.Second)
			t.Cleanup(store.Close)
			if err := store.RegisterBoot(ctx); err != nil {
				t.Fatalf("register executor boot: %v", err)
			}
			lease, err := store.Acquire(ctx, sessionID, "chat")
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if err := lease.Finish(ctx, tc.status, "runtime result"); err != nil {
				t.Fatalf("finish: %v", err)
			}
			var status, activity string
			if err := db.QueryRow(ctx, `SELECT r.status, c.last_turn_result FROM agent_run r
				JOIN ctx_conversation c ON c.session_id = r.session_id WHERE r.id = $1`, lease.Guard.RunID).Scan(&status, &activity); err != nil {
				t.Fatalf("read terminal run: %v", err)
			}
			if status != tc.status || activity != tc.activity {
				t.Fatalf("run=%q activity=%q, want %q/%q", status, activity, tc.status, tc.activity)
			}
		})
	}
}

func TestAbortRequestOutranksCallerStatus(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	q := sqlc.New(db)
	store := agentrun.NewStoreWithLease(ctx, db, bootID, time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "chat")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := store.RequestAbort(ctx, sessionID, "user_stop"); err != nil {
		t.Fatalf("request abort: %v", err)
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, "model finished"); err != nil {
		t.Fatalf("finish after stop request: %v", err)
	}
	row, err := q.GetAgentRun(ctx, lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read aborted run: %v", err)
	}
	if row.Status != agentrun.StatusAborted || row.TerminalReason != "user_stop" {
		t.Fatalf("run = status=%q reason=%q, want aborted/user_stop", row.Status, row.TerminalReason)
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, "second attempt"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("second finish after abort = %v, want ErrLeaseLost", err)
	}
	var activity string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&activity); err != nil {
		t.Fatalf("read aborted activity: %v", err)
	}
	if activity != "canceled" {
		t.Fatalf("aborted activity = %q, want canceled", activity)
	}
}

func TestExpiredRunRecordsErrorActivity(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	q := sqlc.New(db)
	store := agentrun.NewStoreWithLease(ctx, db, bootID, time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "chat")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, lease.Guard.RunID); err != nil {
		t.Fatalf("expire run: %v", err)
	}
	if err := store.Reap(ctx); err != nil {
		t.Fatalf("reap expired run: %v", err)
	}
	row, err := q.GetAgentRun(ctx, lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read expired run: %v", err)
	}
	if row.Status != agentrun.StatusInterrupted || row.TerminalReason != "lease_expired" {
		t.Fatalf("expired run = status=%q reason=%q, want interrupted/lease_expired", row.Status, row.TerminalReason)
	}
	var activity string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&activity); err != nil {
		t.Fatalf("read expired activity: %v", err)
	}
	if activity != "error" {
		t.Fatalf("expired activity = %q, want error", activity)
	}
	// A recovered Run never accepts a late transition from its old owner, and the
	// local renewal for it has stopped.
	if err := lease.Finish(ctx, agentrun.StatusCompleted, "late success"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("late finish after expiry = %v, want ErrLeaseLost", err)
	}
	if got := store.LocalRuns(); got != 0 {
		t.Fatalf("local runs = %d after reaping its own expired Run", got)
	}
}

// The reason a stopped Run records comes from the durable request, not from
// whatever the owner happens to pass when it notices the cancellation.
func TestAbortRecordsTheDurableRequestReason(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	q := sqlc.New(db)
	store := agentrun.NewStoreWithLease(ctx, db, uuid.NewString(), time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "chat")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := store.RequestAbort(ctx, sessionID, "user_stop"); err != nil {
		t.Fatalf("request abort: %v", err)
	}
	if err := lease.Finish(context.WithoutCancel(ctx), agentrun.StatusCanceled, "turn canceled"); err != nil {
		t.Fatalf("finish after stop request: %v", err)
	}
	row, err := q.GetAgentRun(ctx, lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read aborted run: %v", err)
	}
	if row.Status != agentrun.StatusAborted || row.TerminalReason != "user_stop" {
		t.Fatalf("run = status=%q reason=%q, want aborted/user_stop", row.Status, row.TerminalReason)
	}
}
