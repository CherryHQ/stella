package agentrun_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestIndependentStoresAdmitExactlyOneOwner(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	stores := []*agentrun.Store{
		agentrun.NewStoreWithContext(ctx, db, uuid.NewString()),
		agentrun.NewStoreWithContext(ctx, db, uuid.NewString()),
	}
	for _, store := range stores {
		t.Cleanup(store.Close)
		if err := store.RegisterBoot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		lease *agentrun.Lease
		err   error
	}
	results := make(chan result, len(stores))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Go(func() {
			<-start
			lease, err := store.Acquire(ctx, sessionID, "acceptance")
			results <- result{lease, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *agentrun.Lease
	busy := 0
	for got := range results {
		switch {
		case errors.Is(got.err, agentrun.ErrBusy):
			busy++
		case got.err != nil:
			t.Fatalf("admission: %v", got.err)
		case winner != nil:
			t.Fatal("two independent stores admitted the same session")
		default:
			winner = got.lease
		}
	}
	if winner == nil || busy != 1 {
		t.Fatalf("winner=%v busy=%d", winner != nil, busy)
	}
	if err := winner.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

// The party that commits the turn's last durable fact records its own result;
// nothing else (an adapter acknowledgement, a delivery outcome) can change it.
func TestOwnerStatusIsTheRunResult(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status string
		result string
	}{
		{agentrun.StatusCompleted, "success"},
		{agentrun.StatusFailed, "error"},
		{agentrun.StatusCanceled, "canceled"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			sessionID := uuid.NewString()
			if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
				t.Fatal(err)
			}
			lease, err := store.Acquire(ctx, sessionID, "acceptance")
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Finish(ctx, tc.status, "runtime result"); err != nil {
				t.Fatal(err)
			}
			var status, result string
			if err := db.QueryRow(ctx, `SELECT r.status, c.last_turn_result
				FROM agent_run r JOIN ctx_conversation c ON c.session_id = r.session_id
				WHERE r.id = $1`, lease.Guard.RunID).Scan(&status, &result); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || result != tc.result {
				t.Fatalf("run=%q activity=%q, want %q/%q", status, result, tc.status, tc.result)
			}
		})
	}
}

func TestExpiredOwnerCannotWriteOrFinishSuccessor(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	oldStore := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	newStore := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	for _, store := range []*agentrun.Store{oldStore, newStore} {
		t.Cleanup(store.Close)
		if err := store.RegisterBoot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	old, err := oldStore.Acquire(ctx, sessionID, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	oldStore.Close()
	if _, err := db.Exec(ctx, "UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1", old.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	successor, err := newStore.Acquire(ctx, sessionID, "acceptance")
	if err != nil {
		t.Fatalf("successor admission: %v", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := agentrun.ValidateTx(old.ContextWith(ctx), tx); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale write guard = %v", err)
	}
	if err := old.Finish(ctx, agentrun.StatusCompleted, "stale"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale finish = %v", err)
	}
	if err := agentrun.Check(successor.ContextWith(ctx)); err != nil {
		t.Fatalf("stale owner invalidated successor: %v", err)
	}
	if err := successor.Finish(ctx, agentrun.StatusCompleted, "fresh"); err != nil {
		t.Fatal(err)
	}
	var oldStatus, successorStatus string
	if err := db.QueryRow(ctx, "SELECT a.status, b.status FROM agent_run a, agent_run b WHERE a.id=$1 AND b.id=$2", old.Guard.RunID, successor.Guard.RunID).Scan(&oldStatus, &successorStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "interrupted" || successorStatus != "completed" {
		t.Fatalf("old=%s successor=%s", oldStatus, successorStatus)
	}
}

func TestLocalReconciliationObservesAnotherExecutorsTerminalRun(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	owner := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	reaper := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	for _, store := range []*agentrun.Store{owner, reaper} {
		t.Cleanup(store.Close)
		if err := store.RegisterBoot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := owner.Acquire(ctx, sessionID, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1", lease.Guard.RunID); err != nil {
		t.Fatal(err)
	}
	if err := reaper.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	// The remote reaper changed the database and recorded an unknown outcome; this
	// process must stop renewing the Run it can no longer own.
	if err := owner.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if got := owner.LocalRuns(); got != 0 {
		t.Fatalf("local runs = %d after observing a remotely terminalized Run", got)
	}
	if err := agentrun.Check(lease.ContextWith(ctx)); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("terminal owner check = %v", err)
	}
	var status string
	if err := db.QueryRow(ctx, "SELECT status FROM agent_run WHERE id = $1", lease.Guard.RunID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != agentrun.StatusInterrupted {
		t.Fatalf("recovered status = %q, want interrupted", status)
	}
}

func TestGuardSessionMustMatchDurableOwner(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(ctx, sessionID, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	forged := lease.Guard
	forged.SessionID = uuid.NewString()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentrun.ValidateTx(agentrun.WithGuard(ctx, forged), tx); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("mismatched session guard = %v, want ErrLeaseLost", err)
	}
}

func TestOwnershipValidationRetainsLockUntilWriterCommits(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(ctx, sessionID, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if err := agentrun.ValidateTx(lease.ContextWith(ctx), writer); err != nil {
		t.Fatal(err)
	}
	// A terminal transition needs an exclusive row lock. NOWAIT proves the
	// writer still owns its fence without depending on goroutine scheduling.
	_, err = db.Exec(ctx, "SELECT id FROM agent_run WHERE id = $1 FOR UPDATE NOWAIT", lease.Guard.RunID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("terminal lock while writer active = %v, want lock_not_available", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("finish after writer commit: %v", err)
	}
}

func TestClosedStoreRefusesNewAdmission(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, err := store.Acquire(ctx, sessionID, "acceptance"); err == nil {
		t.Fatal("closed store accepted new model work")
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM agent_run WHERE session_id = $1", sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("closed admission left %d durable runs", count)
	}
}

func TestCloseStillDrainsExecutorBoot(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	bootID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO ctx_conversation (session_id) VALUES ($1)", sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, bootID)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := store.DrainBoot(context.Background()); err != nil {
		t.Fatalf("drain after close: %v", err)
	}
	var status string
	if err := db.QueryRow(ctx, "SELECT status FROM runtime_executor_boot WHERE id = $1", bootID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "drained" {
		t.Fatalf("boot status = %q, want drained", status)
	}
	if _, err := store.Acquire(ctx, sessionID, "after-drain"); !errors.Is(err, agentrun.ErrStoreClosed) {
		t.Fatalf("admission after close+drain = %v, want ErrStoreClosed", err)
	}
}
