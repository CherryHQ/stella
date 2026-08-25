package controlsession

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestLeadershipHandsOffWithoutOverlap(t *testing.T) {
	db := dbtest.New(t)
	first, err := Open(t.Context(), db, agentrun.NewBootID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	second, err := Open(t.Context(), db, agentrun.NewBootID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()

	firstCtx, stopFirst := context.WithCancel(t.Context())
	firstStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	var active atomic.Int32
	go func() {
		firstDone <- first.RunLeader(firstCtx, "test-ingress", func(ctx context.Context) {
			active.Add(1)
			close(firstStarted)
			<-ctx.Done()
			active.Add(-1)
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first control session did not acquire leadership")
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	overlapped := make(chan bool, 1)
	go func() {
		secondDone <- second.RunLeader(t.Context(), "test-ingress", func(ctx context.Context) {
			overlapped <- active.Add(1) != 1
			close(secondStarted)
			<-ctx.Done()
			active.Add(-1)
		})
	}()
	select {
	case <-secondStarted:
		t.Fatal("second control session overlapped first leader")
	case <-time.After(1500 * time.Millisecond):
	}

	stopFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first leadership exit = %v", err)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second control session did not take over")
	}
	if <-overlapped {
		t.Fatal("second listener started before the first listener finished draining")
	}
}

func TestControlConnectionLossCancelsLeaderThenReconnectsAndRescans(t *testing.T) {
	db := dbtest.New(t)
	bootID := agentrun.NewBootID()
	session, err := Open(t.Context(), db, bootID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	firstStarted := make(chan struct{})
	firstStopped := make(chan struct{})
	restarted := make(chan struct{})
	done := make(chan error, 1)
	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	defer cancelLeader()
	var starts atomic.Int32
	var active atomic.Int32
	go func() {
		done <- session.RunLeader(leaderCtx, "loss-cancellation", func(ctx context.Context) {
			n := starts.Add(1)
			if active.Add(1) != 1 {
				t.Error("reconnected listener overlapped its predecessor")
			}
			switch n {
			case 1:
				close(firstStarted)
			case 2:
				close(restarted)
			}
			<-ctx.Done()
			active.Add(-1)
			if n == 1 {
				close(firstStopped)
			}
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("control session did not acquire leadership")
	}

	var oldBackendPID int64
	if err := db.QueryRow(t.Context(), `SELECT control_backend_pid FROM runtime_executor_boot WHERE id = $1`, bootID).Scan(&oldBackendPID); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := db.QueryRow(t.Context(), `
		SELECT pg_terminate_backend(control_backend_pid::integer)
		FROM runtime_executor_boot WHERE id = $1
	`, bootID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate control backend: terminated=%v err=%v", terminated, err)
	}
	select {
	case <-firstStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("leader listener was not canceled after control connection loss")
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("control session did not reconnect and reacquire leadership")
	}
	var newBackendPID int64
	if err := db.QueryRow(t.Context(), `SELECT control_backend_pid FROM runtime_executor_boot WHERE id = $1`, bootID).Scan(&newBackendPID); err != nil {
		t.Fatal(err)
	}
	if newBackendPID == oldBackendPID {
		t.Fatalf("control backend PID remained %d after reconnect", oldBackendPID)
	}
	select {
	case err := <-done:
		t.Fatalf("RunLeader returned during recoverable connection loss: %v", err)
	default:
	}
	cancelLeader()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunLeader cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunLeader did not stop after caller cancellation")
	}
}

func TestTransactionPoolingAffinityProbeFailsClosed(t *testing.T) {
	if err := requireBackendAffinity(true); err != nil {
		t.Fatalf("backend-affine probe rejected: %v", err)
	}
	if err := requireBackendAffinity(false); err == nil || !strings.Contains(err.Error(), "transaction-pooling") {
		t.Fatalf("transaction-pooling probe = %v, want explicit rejection", err)
	}
}

func TestMissedNotificationFallsBackToLeadershipScan(t *testing.T) {
	db := dbtest.New(t)
	blocker, err := db.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	key := leadershipKey("missed-notification")
	if _, err := blocker.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, key); err != nil {
		t.Fatal(err)
	}
	// This notification intentionally precedes Session.Open, so the new
	// control session cannot observe it. Leadership must still converge through
	// its periodic full lock scan.
	if _, err := db.Exec(t.Context(), `SELECT pg_notify($1, 'released-soon')`, notificationChannel); err != nil {
		t.Fatal(err)
	}
	session, err := Open(t.Context(), db, agentrun.NewBootID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- session.RunLeader(t.Context(), "missed-notification", func(ctx context.Context) {
			close(started)
			<-ctx.Done()
		})
	}()
	select {
	case <-started:
		t.Fatal("listener started while another backend held leadership")
	case <-time.After(1200 * time.Millisecond):
	}
	if _, err := blocker.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, key); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("missed notification was not repaired by the leadership scan")
	}
}
