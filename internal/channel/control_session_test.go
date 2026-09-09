package channel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestRejectKnownPoolMode(t *testing.T) {
	if err := rejectKnownPoolMode(nil, "transaction"); !errors.Is(err, ErrTransactionPooling) {
		t.Fatalf("explicit transaction pool mode = %v, want ErrTransactionPooling", err)
	}
	config, err := pgx.ParseConfig("postgres://localhost/stella?pool_mode=statement")
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectKnownPoolMode(config, ""); !errors.Is(err, ErrTransactionPooling) {
		t.Fatalf("configured statement pool mode = %v, want ErrTransactionPooling", err)
	}
	if err := rejectKnownPoolMode(nil, "session"); err != nil {
		t.Fatalf("session pool mode = %v, want nil", err)
	}
}

func TestControlSessionSameNameClaimsAreSerialized(t *testing.T) {
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &ControlSession{
		ctx:         runCtx,
		leaderNames: map[string]struct{}{},
		nameChanges: make(chan struct{}),
	}

	releaseFirst, err := session.claimLeaderName(t.Context(), "same-name")
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	secondAcquired := make(chan func(), 1)
	secondErr := make(chan error, 1)
	go func() {
		release, err := session.claimLeaderName(secondCtx, "same-name")
		if err != nil {
			secondErr <- err
			return
		}
		secondAcquired <- release
	}()

	select {
	case <-secondAcquired:
		t.Fatal("same-name leader claim was reentrant")
	case <-secondErr:
		t.Fatal("same-name leader claim failed before release")
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()
	var releaseSecond func()
	select {
	case releaseSecond = <-secondAcquired:
	case err := <-secondErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("same-name leader claim did not proceed after release")
	}
	releaseSecond()
}

func TestControlSessionLeadershipHandsOffWithoutOverlap(t *testing.T) {
	db := dbtest.New(t)
	first, err := OpenControlSession(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	second, err := OpenControlSession(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()

	firstCtx, stopFirst := context.WithCancel(t.Context())
	defer stopFirst()
	firstStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	var active atomic.Int32
	go func() {
		firstDone <- first.RunLeader(firstCtx, "test-channel-ingress", func(ctx context.Context) {
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
	overlapped := make(chan bool, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.RunLeader(t.Context(), "test-channel-ingress", func(ctx context.Context) {
			overlapped <- active.Add(1) != 1
			close(secondStarted)
			<-ctx.Done()
			active.Add(-1)
		})
	}()
	select {
	case <-secondStarted:
		t.Fatal("second control session overlapped first leader")
	case <-time.After(1200 * time.Millisecond):
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
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("second leadership exit = %v", err)
	}
}

func TestControlSessionSameNameLeadershipDoesNotOverlap(t *testing.T) {
	db := dbtest.New(t)
	session, err := OpenControlSession(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	firstStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- session.RunLeader(firstCtx, "same-name", func(ctx context.Context) {
			close(firstStarted)
			<-ctx.Done()
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first same-name leader did not start")
	}

	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- session.RunLeader(secondCtx, "same-name", func(ctx context.Context) {
			close(secondStarted)
			<-ctx.Done()
		})
	}()
	select {
	case <-secondStarted:
		t.Fatal("same-name leader callbacks overlapped on one control session")
	case <-time.After(1200 * time.Millisecond):
	}

	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first same-name leadership exit = %v", err)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second same-name leader did not start after first stopped")
	}
	cancelSecond()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("second same-name leadership exit = %v", err)
	}
}

func TestControlSessionCloseWaitsForLeaderCleanupBeforeUnlock(t *testing.T) {
	db := dbtest.New(t)
	first, err := OpenControlSession(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	second, err := OpenControlSession(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()

	firstStarted := make(chan struct{})
	cleanupStarted := make(chan struct{})
	allowCleanup := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.RunLeader(t.Context(), "close-drain", func(ctx context.Context) {
			close(firstStarted)
			<-ctx.Done()
			close(cleanupStarted)
			<-allowCleanup
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first close-drain leader did not start")
	}

	secondStarted := make(chan struct{})
	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.RunLeader(secondCtx, "close-drain", func(ctx context.Context) {
			close(secondStarted)
			<-ctx.Done()
		})
	}()

	closeCtx, cancelClose := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelClose()
	if err := first.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while callback is draining = %v, want deadline", err)
	}
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("leader callback did not enter cleanup")
	}
	select {
	case <-secondStarted:
		t.Fatal("second leader acquired lock before first callback cleanup")
	case <-time.After(300 * time.Millisecond):
	}

	close(allowCleanup)
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first close-drain leadership exit = %v", err)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second leader did not acquire lock after first cleanup")
	}
	cancelSecond()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("second close-drain leadership exit = %v", err)
	}
}

func TestControlSessionConnectionLossCancelsLeaderAndReconnects(t *testing.T) {
	db := dbtest.New(t)
	session, err := OpenControlSession(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	firstStarted := make(chan struct{})
	firstStopped := make(chan struct{})
	restarted := make(chan struct{})
	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	defer cancelLeader()
	var starts atomic.Int32
	var active atomic.Int32
	done := make(chan error, 1)
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

	oldPID, err := session.BackendPID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := db.QueryRow(t.Context(), `SELECT pg_terminate_backend($1)`, oldPID).Scan(&terminated); err != nil || !terminated {
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
	newPID, err := session.BackendPID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if newPID == oldPID {
		t.Fatalf("control backend PID remained %d after reconnect", oldPID)
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

func TestControlSessionMissedNotificationFallsBackToLockScan(t *testing.T) {
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
	if _, err := db.Exec(t.Context(), `SELECT pg_notify($1, 'released-soon')`, ControlNotificationChannel); err != nil {
		t.Fatal(err)
	}
	session, err := OpenControlSession(t.Context(), db)
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
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunLeader exit = %v", err)
	}
}
