package agentrun_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func newRunFixture(t *testing.T) (*agentrun.Store, *agentrun.Lease, *pgxpool.Pool, context.Context) {
	t.Helper()
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
	lease, err := store.Acquire(ctx, sessionID, "lifecycle-test")
	if err != nil {
		t.Fatal(err)
	}
	return store, lease, db, ctx
}

// A rolled-back terminal transaction must leave the Run running and still
// renewed, so the caller can record the failure through the ordinary path.
func TestFinishWithRollbackKeepsRunRunningAndRenewing(t *testing.T) {
	store, lease, db, ctx := newRunFixture(t)
	handoffErr := errors.New("source commit failed")

	err := lease.FinishWith(ctx, agentrun.StatusCompleted, "should not commit", func(context.Context, pgx.Tx, string) error {
		return handoffErr
	})
	if !errors.Is(err, handoffErr) {
		t.Fatalf("FinishWith = %v, want the caller's handoff error", err)
	}
	var status string
	if err := db.QueryRow(ctx, "SELECT status FROM agent_run WHERE id = $1", lease.Guard.RunID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("status = %q after a rolled-back handoff, want running", status)
	}
	if store.LocalRuns() != 1 {
		t.Fatalf("local runs = %d, want the Run still renewed", store.LocalRuns())
	}
	// The owner can still finish it afterwards, which proves the lease survived.
	if err := lease.Finish(ctx, agentrun.StatusFailed, "handoff failed"); err != nil {
		t.Fatalf("finish after rollback: %v", err)
	}
}

// A successful handoff commits the caller's write and the terminal transition
// together, and only then stops renewing the Run.
func TestFinishWithCommitsCallerWriteAndTerminalStateTogether(t *testing.T) {
	store, lease, db, ctx := newRunFixture(t)
	var writtenStatus string

	if err := lease.FinishWith(ctx, agentrun.StatusCompleted, "turn completed", func(ctx context.Context, tx pgx.Tx, written string) error {
		writtenStatus = written
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM agent_run WHERE id = $1 AND status = 'completed'", lease.Guard.RunID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("handoff did not observe its own terminal write")
		}
		return nil
	}); err != nil {
		t.Fatalf("FinishWith: %v", err)
	}
	if writtenStatus != agentrun.StatusCompleted {
		t.Fatalf("written status = %q, want completed", writtenStatus)
	}
	var status, activity string
	if err := db.QueryRow(ctx, `SELECT r.status, c.last_turn_result FROM agent_run r
		JOIN ctx_conversation c ON c.session_id = r.session_id WHERE r.id = $1`, lease.Guard.RunID).Scan(&status, &activity); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || activity != "success" {
		t.Fatalf("run=%q activity=%q, want completed/success", status, activity)
	}
	waitForLocalRelease(t, store)
}

// A lease-derived context that is already canceled (an explicit stop cancels it)
// must not prevent the terminal write: a stopped turn still records its result.
func TestFinishWithSurvivesCanceledLeaseContext(t *testing.T) {
	store, lease, db, ctx := newRunFixture(t)
	if _, err := store.RequestAbort(ctx, lease.Guard.SessionID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	waitForCause(t, lease)

	// A durable stop request outranks the caller's status inside the statement.
	written := ""
	err := lease.FinishWith(lease.Context(), agentrun.StatusCompleted, "model finished", func(_ context.Context, _ pgx.Tx, status string) error {
		written = status
		return nil
	})
	if err != nil {
		t.Fatalf("FinishWith with a canceled lease context: %v", err)
	}
	if written != agentrun.StatusAborted {
		t.Fatalf("written status = %q, want aborted (the stop request outranks the caller)", written)
	}
	var status, activity string
	if err := db.QueryRow(ctx, `SELECT r.status, c.last_turn_result FROM agent_run r
		JOIN ctx_conversation c ON c.session_id = r.session_id WHERE r.id = $1`, lease.Guard.RunID).Scan(&status, &activity); err != nil {
		t.Fatal(err)
	}
	if status != "aborted" || activity != "canceled" {
		t.Fatalf("run=%q activity=%q, want aborted/canceled", status, activity)
	}
}

// A terminal write on a transaction that has already failed must not report
// success, and must not leave the Run settled locally.
func TestTerminalTxOnFailedTransactionIsRejected(t *testing.T) {
	_, lease, db, ctx := newRunFixture(t)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := agentrun.TerminalTx(ctx, tx, lease.Guard, agentrun.StatusCompleted, "on a dead transaction"); err == nil {
		t.Fatal("TerminalTx on a failed transaction reported success")
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, "real finish"); err != nil {
		t.Fatalf("finish after failed attempt: %v", err)
	}
}

// A second terminal transition from the same owner is refused by the row, not by
// a local flag.
func TestSecondFinishIsRejectedByTheDurableRow(t *testing.T) {
	_, lease, _, ctx := newRunFixture(t)
	if err := lease.Finish(ctx, agentrun.StatusCompleted, "first"); err != nil {
		t.Fatal(err)
	}
	if err := lease.Finish(ctx, agentrun.StatusFailed, "second"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("second finish = %v, want ErrLeaseLost", err)
	}
}

func TestClosingStoreRollsBackPendingTerminalWrite(t *testing.T) {
	store, lease, db, ctx := newRunFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- lease.FinishWith(ctx, agentrun.StatusCompleted, "must roll back", func(context.Context, pgx.Tx, string) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	closed := make(chan struct{})
	go func() {
		store.Close()
		close(closed)
	}()
	<-lease.Context().Done()
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("terminal write committed after lifecycle cancellation")
	}
	<-closed
	var status string
	if err := db.QueryRow(ctx, "SELECT status FROM agent_run WHERE id = $1", lease.Guard.RunID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("uncommitted terminal write persisted as %s", status)
	}
}

func TestTerminalTransitionRequiresMatchingSession(t *testing.T) {
	_, lease, db, ctx := newRunFixture(t)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	guard := lease.Guard
	guard.SessionID = uuid.NewString()
	if _, err := agentrun.TerminalTx(ctx, tx, guard, agentrun.StatusCompleted, "wrong session"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("wrong-session terminal write = %v", err)
	}
}

func waitForLocalRelease(t *testing.T, store *agentrun.Store) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for store.LocalRuns() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("store still renews %d runs after a committed terminal transition", store.LocalRuns())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForCause(t *testing.T, lease *agentrun.Lease) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for lease.Context().Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("lease context was not canceled by the stop request")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
