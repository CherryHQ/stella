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
	"github.com/CherryHQ/stella/pkg/runcontrol"
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
	// one-second database lease. PrepareCompletion proves the lease kept renewing.
	time.Sleep(800 * time.Millisecond)
	if err := lease.Completion().Check(ctx); err != nil {
		t.Fatalf("ownership lost during slow tail: %v", err)
	}
	if err := lease.PrepareCompletion(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("prepare completion after slow tail: %v", err)
	}
	if err := lease.Completion().Ack(ctx, runcontrol.OutcomeDelivered); err != nil {
		t.Fatalf("ack completion: %v", err)
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

func TestCompletionFailureIsDurableAndConflictingAckRejected(t *testing.T) {
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
	if err := lease.PrepareCompletion(ctx, agentrun.StatusCompleted, "model finished"); err != nil {
		t.Fatalf("prepare completion: %v", err)
	}
	completion := lease.Completion()
	if err := completion.Ack(ctx, runcontrol.OutcomeFailed); err != nil {
		t.Fatalf("ack failed outcome: %v", err)
	}
	row, err := q.GetAgentRun(ctx, lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read completed run: %v", err)
	}
	if row.Status != agentrun.StatusFailed || row.CompletionState != "acked" || row.CompletionOutcome != string(runcontrol.OutcomeFailed) {
		t.Fatalf("failed completion row = status=%q state=%q outcome=%q", row.Status, row.CompletionState, row.CompletionOutcome)
	}
	if err := completion.Ack(ctx, runcontrol.OutcomeFailed); err != nil {
		t.Fatalf("same failed outcome is not idempotent: %v", err)
	}
	if err := completion.Ack(ctx, runcontrol.OutcomeDelivered); !errors.Is(err, agentrun.ErrCompletionConflict) {
		t.Fatalf("conflicting delivered outcome = %v, want ErrCompletionConflict", err)
	}
}

func TestTerminalTransitionUpdatesSessionActivityAtomically(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	store := agentrun.NewStoreWithLease(ctx, db, bootID, time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "chat")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, "done"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	var result string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&result); err != nil {
		t.Fatalf("read session activity: %v", err)
	}
	if result != "success" {
		t.Fatalf("last_turn_result = %q, want success", result)
	}
}

func TestAckRecordsFailedEgress(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	store := agentrun.NewStoreWithLease(ctx, db, bootID, time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "chat")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.PrepareCompletion(ctx, agentrun.StatusCompleted, "model done"); err != nil {
		t.Fatalf("prepare completion: %v", err)
	}
	if err := lease.Completion().Ack(ctx, runcontrol.OutcomeFailed); err != nil {
		t.Fatalf("ack with activity: %v", err)
	}
	var status, result string
	if err := db.QueryRow(ctx, `SELECT status, last_turn_result FROM agent_run run JOIN ctx_conversation c ON c.session_id = run.session_id WHERE run.id = $1`, lease.Guard.RunID).Scan(&status, &result); err != nil {
		t.Fatalf("read terminal activity: %v", err)
	}
	if status != agentrun.StatusFailed || result != "error" {
		t.Fatalf("status=%q activity=%q, want failed/error", status, result)
	}
}

func TestEarlyUnknownAckTerminalizesOpenRun(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	store := agentrun.NewStoreWithLease(ctx, db, bootID, time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "chat")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Completion().Ack(ctx, runcontrol.OutcomeUnknown); err != nil {
		t.Fatalf("early unknown ack: %v", err)
	}
	select {
	case <-lease.Completion().Done():
	case <-time.After(time.Second):
		t.Fatal("completion barrier did not close after early unknown ack")
	}
	var status, state, outcome, activity string
	if err := db.QueryRow(ctx, `SELECT run.status, run.completion_state, run.completion_outcome, c.last_turn_result FROM agent_run run JOIN ctx_conversation c ON c.session_id = run.session_id WHERE run.id = $1`, lease.Guard.RunID).Scan(&status, &state, &outcome, &activity); err != nil {
		t.Fatalf("read terminal run: %v", err)
	}
	if status != agentrun.StatusInterrupted || state != "acked" || outcome != string(runcontrol.OutcomeUnknown) {
		t.Fatalf("terminal run = status=%q state=%q outcome=%q", status, state, outcome)
	}
	if activity != "error" {
		t.Fatalf("early unknown activity = %q, want error", activity)
	}
	if err := lease.Completion().Ack(ctx, runcontrol.OutcomeUnknown); err != nil {
		t.Fatalf("repeated unknown ack is not idempotent: %v", err)
	}
	if err := lease.PrepareCompletion(ctx, agentrun.StatusCompleted, "late prepare"); !errors.Is(err, agentrun.ErrOutcomeUnknown) {
		t.Fatalf("prepare after durable unknown = %v, want ErrOutcomeUnknown", err)
	}
	if err := lease.Completion().Check(ctx); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("ownership check after unknown terminal = %v, want ErrLeaseLost", err)
	}
}

func TestAbortRequestedReadyCompletionBecomesUnknown(t *testing.T) {
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
	if err := lease.PrepareCompletion(ctx, agentrun.StatusCompleted, "model finished"); err != nil {
		t.Fatalf("prepare completion: %v", err)
	}
	if _, err := store.RequestAbort(ctx, sessionID, "user_stop"); err != nil {
		t.Fatalf("request abort: %v", err)
	}
	if err := store.Reap(ctx); err != nil {
		t.Fatalf("reap abort: %v", err)
	}
	row, err := q.GetAgentRun(ctx, lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read aborted run: %v", err)
	}
	if row.Status != agentrun.StatusAborted || row.CompletionState != "unknown" || row.CompletionOutcome != string(runcontrol.OutcomeUnknown) || row.CompletionAckedAt.Valid {
		t.Fatalf("aborted ready completion row = status=%q state=%q outcome=%q acked_at=%v", row.Status, row.CompletionState, row.CompletionOutcome, row.CompletionAckedAt)
	}
	if err := lease.Completion().Ack(ctx, runcontrol.OutcomeUnknown); err != nil {
		t.Fatalf("same unknown outcome acknowledgement = %v, want nil", err)
	}
	var activity string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&activity); err != nil {
		t.Fatalf("read aborted activity: %v", err)
	}
	if activity != "canceled" {
		t.Fatalf("aborted ready activity = %q, want canceled", activity)
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
	if row.Status != agentrun.StatusInterrupted || row.CompletionState != "unknown" || row.CompletionOutcome != string(runcontrol.OutcomeUnknown) || row.CompletionAckedAt.Valid {
		t.Fatalf("expired run = status=%q state=%q outcome=%q acked_at=%v", row.Status, row.CompletionState, row.CompletionOutcome, row.CompletionAckedAt)
	}
	var activity string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&activity); err != nil {
		t.Fatalf("read expired activity: %v", err)
	}
	if activity != "error" {
		t.Fatalf("expired activity = %q, want error", activity)
	}
	if err := lease.Completion().Ack(ctx, runcontrol.OutcomeUnknown); err != nil {
		t.Fatalf("ack persisted expiry unknown: %v", err)
	}
}

func TestLeaseAbortAfterRequestTerminalizesOpenRun(t *testing.T) {
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
	if err := lease.Abort(context.WithoutCancel(ctx)); err != nil {
		t.Fatalf("abort: %v", err)
	}
	row, err := q.GetAgentRun(ctx, lease.Guard.RunID)
	if err != nil {
		t.Fatalf("read aborted run: %v", err)
	}
	if row.Status != agentrun.StatusAborted || row.CompletionState != "acked" || row.CompletionOutcome != string(runcontrol.OutcomeDiscarded) {
		t.Fatalf("aborted open run = status=%q state=%q outcome=%q", row.Status, row.CompletionState, row.CompletionOutcome)
	}
	var activity string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&activity); err != nil {
		t.Fatalf("read aborted activity: %v", err)
	}
	if activity != "canceled" {
		t.Fatalf("aborted open activity = %q, want canceled", activity)
	}
	if err := lease.Abort(context.WithoutCancel(ctx)); err != nil {
		t.Fatalf("repeated abort is not idempotent: %v", err)
	}
}

func TestLeaseAbortUsesDurableRequestReason(t *testing.T) {
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
	if _, err := store.RequestAbort(ctx, sessionID, "user_stop"); err != nil {
		t.Fatalf("request abort: %v", err)
	}
	if err := lease.Abort(context.WithoutCancel(ctx)); err != nil {
		t.Fatalf("abort should use the durable request reason: %v", err)
	}
}
