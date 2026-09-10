package sessionexecution_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/sessionexecution"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/core/agenterr"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func seedSession(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	_, err := sqlc.New(db).CreateConversation(t.Context(), sqlc.CreateConversationParams{ID: id, SessionID: id, Kind: "chat", LastActive: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func claim(t *testing.T, store *sessionexecution.Store, id string) (context.Context, *sessionexecution.Lease) {
	t.Helper()
	ctx, lease, err := store.Claim(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Finish("error") })
	return ctx, lease
}

func stopHeartbeat(l *sessionexecution.Lease) { sessionexecution.StopHeartbeatForTest(l) }

func expire(t *testing.T, db *pgxpool.Pool, lease *sessionexecution.Lease) {
	t.Helper()
	stopHeartbeat(lease)
	if _, err := db.Exec(t.Context(), "UPDATE ctx_session_execution SET lease_until = clock_timestamp() - interval '1 second' WHERE session_id=$1 AND token=$2", sessionexecution.LeaseSessionIDForTest(lease), sessionexecution.LeaseTokenForTest(lease)); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentPoolsCompeteAndStaleTokenCannotWrite(t *testing.T) {
	db := dbtest.New(t)
	other, err := pgxpool.NewWithConfig(t.Context(), db.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	id := seedSession(t, db)
	start := make(chan struct{})
	type result struct {
		ctx   context.Context
		lease *sessionexecution.Lease
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, pool := range []*pgxpool.Pool{db, other} {
		wg.Go(func() {
			<-start
			ctx, lease, err := sessionexecution.New(pool).Claim(t.Context(), id)
			results <- result{ctx, lease, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var winner result
	busy := 0
	for r := range results {
		if errors.Is(r.err, agenterr.ErrSessionBusy) {
			busy++
			continue
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		winner = r
	}
	if winner.lease == nil || busy != 1 {
		t.Fatalf("winner=%v busy=%d", winner.lease, busy)
	}
	defer func() { _ = winner.lease.Finish("error") }()
	expire(t, db, winner.lease)
	_, next := claim(t, sessionexecution.New(other), id)
	if sessionexecution.LeaseTokenForTest(next) == sessionexecution.LeaseTokenForTest(winner.lease) {
		t.Fatal("successor reused token")
	}
	if err := winner.lease.Renew(t.Context()); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("stale renew: %v", err)
	}
	err = sessionexecution.Exec(context.WithoutCancel(winner.ctx), db, func(ctx context.Context, q *sqlc.Queries) error {
		return q.FinishSessionExecutionActivity(ctx, sqlc.FinishSessionExecutionActivityParams{SessionID: id})
	})
	if !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("stale write: %v", err)
	}
	if err := winner.lease.Finish("success"); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("stale finish: %v", err)
	}
	n, err := sqlc.New(db).CancelSessionExecution(t.Context(), sqlc.CancelSessionExecutionParams{SessionID: id, Token: sessionexecution.LeaseTokenForTest(winner.lease)})
	if err != nil || n != 0 {
		t.Fatalf("old cancel affected successor: %d %v", n, err)
	}
	if err := next.Finish("success"); err != nil {
		t.Fatal(err)
	}
	var resultText string
	if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id=$1", id).Scan(&resultText); err != nil || resultText != "success" {
		t.Fatalf("result=%s err=%v", resultText, err)
	}
}

func TestGuardedTransactionBlocksTakeoverButNotOtherSessions(t *testing.T) {
	db := dbtest.New(t)
	store := sessionexecution.New(db)
	ctx, lease := claim(t, store, seedSession(t, db))
	stopHeartbeat(lease)
	tx, err := sessionexecution.Begin(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	// Expire within the already admitted writer. The takeover must wait for
	// this transaction, then recheck the committed expiration under its lock.
	if _, err := tx.Exec(ctx, "UPDATE ctx_session_execution SET lease_until=clock_timestamp()-interval '1 second' WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(lease)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		lease *sessionexecution.Lease
		err   error
	}
	taken := make(chan result, 1)
	go func() {
		_, next, err := store.Claim(t.Context(), sessionexecution.LeaseSessionIDForTest(lease))
		taken <- result{next, err}
	}()
	waitForLock(t, db)
	_, unrelated := claim(t, store, seedSession(t, db))
	if err := unrelated.Finish("success"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-taken:
		t.Fatal("takeover passed active writer")
	default:
	}
	if _, err := tx.Exec(ctx, "UPDATE ctx_conversation SET title='old writer committed' WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(lease)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	next := <-taken
	if next.err != nil {
		t.Fatal(next.err)
	}
	defer func() { _ = next.lease.Finish("error") }()
	var title string
	if err := db.QueryRow(t.Context(), "SELECT title FROM ctx_conversation WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(lease)).Scan(&title); err != nil || title != "old writer committed" {
		t.Fatalf("title=%s err=%v", title, err)
	}
}

func waitForLock(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("takeover did not wait for writer")
		case <-tick.C:
		}
	}
}

func TestCancelAndFinishOrders(t *testing.T) {
	db := dbtest.New(t)
	store := sessionexecution.New(db)
	ctx, lease := claim(t, store, seedSession(t, db))
	canceled, err := store.CancelCurrent(t.Context(), sessionexecution.LeaseSessionIDForTest(lease))
	if err != nil || !canceled {
		t.Fatalf("cancel=%v %v", canceled, err)
	}
	if _, err := sessionexecution.Begin(context.WithoutCancel(ctx), db); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("canceled write: %v", err)
	}
	if err := lease.Finish("success"); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(lease)).Scan(&outcome); err != nil || outcome != "canceled" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	_, next := claim(t, store, sessionexecution.LeaseSessionIDForTest(lease))
	if err := next.Finish("success"); err != nil {
		t.Fatal(err)
	}
	canceled, err = store.CancelCurrent(t.Context(), sessionexecution.LeaseSessionIDForTest(next))
	if err != nil || canceled {
		t.Fatalf("cancel after finish=%v %v", canceled, err)
	}
}

func TestCanceledContextCannotBypassGuardAndMissingTokenFailsClosed(t *testing.T) {
	db := dbtest.New(t)
	ctx, lease := claim(t, sessionexecution.New(db), seedSession(t, db))
	sessionexecution.CancelLeaseForTest(lease, context.Canceled)
	if _, err := sessionexecution.Begin(context.WithoutCancel(ctx), db); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("WithoutCancel bypassed lease: %v", err)
	}
	if _, err := sessionexecution.Begin(sessionexecution.Require(t.Context()), db); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("missing token accepted: %v", err)
	}
	if err := lease.Finish("error"); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(lease)).Scan(&result); err != nil || result != "canceled" {
		t.Fatalf("local cancellation result=%s error=%v", result, err)
	}
	// Independent user and accepted recovery transactions remain valid.
	tx, err := sessionexecution.Begin(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRenewCannotResurrectAndReapPreservesCompletedActivity(t *testing.T) {
	db := dbtest.New(t)
	store := sessionexecution.New(db)
	_, canceled := claim(t, store, seedSession(t, db))
	if _, err := store.CancelCurrent(t.Context(), sessionexecution.LeaseSessionIDForTest(canceled)); err != nil {
		t.Fatal(err)
	}
	expire(t, db, canceled)
	if err := canceled.Renew(t.Context()); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("expired renew: %v", err)
	}
	_, completed := claim(t, store, seedSession(t, db))
	if _, err := db.Exec(t.Context(), "UPDATE ctx_conversation SET last_turn_result='success', last_turn_completed_at=clock_timestamp() WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(completed)); err != nil {
		t.Fatal(err)
	}
	expire(t, db, completed)
	if err := store.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ id, result string }{{sessionexecution.LeaseSessionIDForTest(canceled), "canceled"}, {sessionexecution.LeaseSessionIDForTest(completed), "success"}} {
		var got string
		if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id=$1", want.id).Scan(&got); err != nil || got != want.result {
			t.Fatalf("result=%s want=%s err=%v", got, want.result, err)
		}
		if _, err := sqlc.New(db).GetSessionExecution(t.Context(), want.id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("execution survived reap: %v", err)
		}
	}
}

func TestFinishLostCommitAcknowledgmentDoesNotRepeat(t *testing.T) {
	db := dbtest.New(t)
	store := sessionexecution.New(db)
	_, lease := claim(t, store, seedSession(t, db))
	commits := 0
	sessionexecution.SetFinishCommitForTest(store, func(ctx context.Context, tx pgx.Tx) error {
		commits++
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return io.EOF
	})
	if err := lease.Finish("success"); !errors.Is(err, sessionexecution.ErrOutcomeUnknown) {
		t.Fatalf("commit result: %v", err)
	}
	if err := lease.Finish("success"); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("retry result: %v", err)
	}
	if commits != 1 {
		t.Fatalf("commits=%d", commits)
	}
	var outcome string
	if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id=$1", sessionexecution.LeaseSessionIDForTest(lease)).Scan(&outcome); err != nil || outcome != "success" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
}

func TestLeaseValidityIsCheckedAfterRowLockWait(t *testing.T) {
	for _, operation := range []string{"renew", "write", "finish"} {
		t.Run(operation, func(t *testing.T) {
			db := dbtest.New(t)
			ctx, lease := claim(t, sessionexecution.New(db), seedSession(t, db))
			stopHeartbeat(lease)
			id := sessionexecution.LeaseSessionIDForTest(lease)
			if _, err := db.Exec(t.Context(), "UPDATE ctx_session_execution SET lease_until=clock_timestamp()+interval '2 seconds' WHERE session_id=$1", id); err != nil {
				t.Fatal(err)
			}
			blocker, err := db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(t.Context()) }()
			if _, err := blocker.Exec(t.Context(), "SELECT token FROM ctx_session_execution WHERE session_id=$1 FOR UPDATE", id); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "renew":
					done <- lease.Renew(t.Context())
				case "finish":
					done <- lease.Finish("success")
				default:
					tx, err := sessionexecution.Begin(ctx, db)
					if err == nil {
						_ = tx.Rollback(t.Context())
					}
					done <- err
				}
			}()
			waitForLock(t, db)
			// The blocker changes no tuple. PostgreSQL must evaluate its clock
			// after the waiter acquires the lock, not reuse a pre-wait predicate.
			if _, err := blocker.Exec(t.Context(), "SELECT pg_sleep(GREATEST(EXTRACT(EPOCH FROM lease_until-clock_timestamp())::double precision + 0.02, 0)) FROM ctx_session_execution WHERE session_id=$1", id); err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, sessionexecution.ErrLost) {
				t.Fatalf("%s revived expired execution: %v", operation, err)
			}
		})
	}
}

func TestChildClaimUsesOwnTokenAndPreservesParent(t *testing.T) {
	db := dbtest.New(t)
	store := sessionexecution.New(db)
	parentCtx, parent := claim(t, store, seedSession(t, db))
	childCtx, child, err := store.Claim(parentCtx, seedSession(t, db))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Finish("error") }()
	if sessionexecution.FromContext(parentCtx) != parent || sessionexecution.FromContext(childCtx) != child {
		t.Fatal("child claim replaced parent context")
	}
	if sessionexecution.LeaseTokenForTest(parent) == sessionexecution.LeaseTokenForTest(child) {
		t.Fatal("child reused parent token")
	}
	if err := child.Finish("success"); err != nil {
		t.Fatal(err)
	}
	if err := parent.Renew(t.Context()); err != nil {
		t.Fatalf("child completion stopped parent: %v", err)
	}
}
