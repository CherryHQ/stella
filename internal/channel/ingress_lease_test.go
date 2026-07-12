package channel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TestMain for this package lives in identity_test.go (dbtest.Main).

// leaseObserver records onAcquire/onRelease calls and tracks whether the lease
// currently believes it is the leader.
type leaseObserver struct {
	mu        sync.Mutex
	acquires  int
	releases  int
	acquired  bool
	leaderCtx context.Context
}

func (o *leaseObserver) onAcquire(leaderCtx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.acquires++
	o.acquired = true
	o.leaderCtx = leaderCtx
}

func (o *leaseObserver) onRelease() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.releases++
	o.acquired = false
}

func (o *leaseObserver) snapshot() (acquires, releases int, acquired bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.acquires, o.releases, o.acquired
}

// waitFor polls cond until it is true or the deadline elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestIngressLeaseSingleLeader: two leases with different owners contend; only
// one becomes leader, and the loser never fires onAcquire while the holder lives.
func TestIngressLeaseSingleLeader(t *testing.T) {
	pool := dbtest.New(t)
	ttl := 600 * time.Millisecond

	leaseA := NewIngressLease(pool, "owner-A", ttl, nil)
	leaseB := NewIngressLease(pool, "owner-B", ttl, nil)
	obsA := &leaseObserver{}
	obsB := &leaseObserver{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = leaseA.Run(ctx, obsA.onAcquire, obsA.onRelease) }()
	go func() { defer wg.Done(); _ = leaseB.Run(ctx, obsB.onAcquire, obsB.onRelease) }()

	// One of the two must acquire promptly.
	if !waitFor(t, 2*time.Second, func() bool {
		_, _, a := obsA.snapshot()
		_, _, b := obsB.snapshot()
		return a || b
	}) {
		t.Fatal("no replica acquired leadership")
	}

	// Give both a few renew cycles; leadership must stay exclusive throughout.
	deadline := time.Now().Add(ttl * 3)
	for time.Now().Before(deadline) {
		_, _, a := obsA.snapshot()
		_, _, b := obsB.snapshot()
		if a && b {
			t.Fatal("both replicas held leadership simultaneously")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Exactly one acquired, exactly once; the other never did.
	acqA, _, _ := obsA.snapshot()
	acqB, _, _ := obsB.snapshot()
	if acqA+acqB != 1 {
		t.Fatalf("expected exactly one acquisition total, got A=%d B=%d", acqA, acqB)
	}

	cancel()
	wg.Wait()
}

// TestIngressLeaseFailoverOnExpiry: a crashed holder (acquired, then neither
// renews nor releases) has its lease taken over by a standby after expiry, within
// ~ttl. This is the failover-on-crash path that has no graceful release.
func TestIngressLeaseFailoverOnExpiry(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	ttl := 700 * time.Millisecond

	// Simulate a crashed holder: seed a live lease via the raw CAS and never touch
	// it again (no renew, no release). It can only be reclaimed by expiry.
	seeded, err := sqlc.New(pool).AcquireChannelIngressLease(ctx, sqlc.AcquireChannelIngressLeaseParams{
		OwnerID:    "owner-dead",
		TtlSeconds: ttl.Seconds(),
	})
	if err != nil {
		t.Fatalf("seed crashed holder lease: %v", err)
	}
	if seeded.OwnerID != "owner-dead" {
		t.Fatalf("seed owner = %q, want owner-dead", seeded.OwnerID)
	}

	standby := NewIngressLease(pool, "owner-standby", ttl, nil)
	obsStandby := &leaseObserver{}
	standbyCtx, stopStandby := context.WithCancel(context.Background())
	defer stopStandby()
	standbyDone := make(chan struct{})
	go func() {
		defer close(standbyDone)
		_ = standby.Run(standbyCtx, obsStandby.onAcquire, obsStandby.onRelease)
	}()

	// Before expiry the standby must NOT acquire.
	time.Sleep(ttl / 3)
	if _, _, a := obsStandby.snapshot(); a {
		t.Fatal("standby acquired leadership before the crashed holder's lease expired")
	}

	// After expiry, the standby takes over within a small multiple of ttl.
	if !waitFor(t, ttl*3, func() bool { _, _, a := obsStandby.snapshot(); return a }) {
		t.Fatal("standby did not acquire leadership after the crashed lease expired")
	}
	acq, _, _ := obsStandby.snapshot()
	if acq != 1 {
		t.Fatalf("standby onAcquire fired %d times, want 1", acq)
	}

	stopStandby()
	<-standbyDone
}

// TestIngressLeaseReleaseFreesImmediately: a graceful release frees the lease so
// a peer acquires without waiting for expiry.
func TestIngressLeaseReleaseFreesImmediately(t *testing.T) {
	pool := dbtest.New(t)
	ttl := 2 * time.Second // long ttl: only a real release (not expiry) can free it fast

	holder := NewIngressLease(pool, "owner-holder", ttl, nil)
	obsHolder := &leaseObserver{}
	holderCtx, stopHolder := context.WithCancel(context.Background())
	holderDone := make(chan struct{})
	go func() { defer close(holderDone); _ = holder.Run(holderCtx, obsHolder.onAcquire, obsHolder.onRelease) }()
	if !waitFor(t, 2*time.Second, func() bool { _, _, a := obsHolder.snapshot(); return a }) {
		t.Fatal("holder never acquired leadership")
	}

	standby := NewIngressLease(pool, "owner-standby", ttl, nil)
	obsStandby := &leaseObserver{}
	standbyCtx, stopStandby := context.WithCancel(context.Background())
	defer stopStandby()
	standbyDone := make(chan struct{})
	go func() {
		defer close(standbyDone)
		_ = standby.Run(standbyCtx, obsStandby.onAcquire, obsStandby.onRelease)
	}()

	// Gracefully stop the holder; Run releases the lease on ctx cancel.
	start := time.Now()
	stopHolder()
	<-holderDone

	// The standby renews at ttl/3; a freed lease lets it acquire on its next tick,
	// well before the 2s ttl would have expired.
	if !waitFor(t, ttl, func() bool { _, _, a := obsStandby.snapshot(); return a }) {
		t.Fatal("standby did not acquire after graceful release")
	}
	if elapsed := time.Since(start); elapsed >= ttl {
		t.Fatalf("takeover took %v, expected well under ttl %v (release did not free the lease)", elapsed, ttl)
	}

	stopStandby()
	<-standbyDone
}

// TestAcquireCASRejectsSecondAcquirer: the raw CAS query rejects a second owner
// while a live lease is held, and admits it once released/expired.
func TestAcquireCASRejectsSecondAcquirer(t *testing.T) {
	pool := dbtest.New(t)
	q := sqlc.New(pool)
	ctx := context.Background()

	// First acquirer takes a 60s lease.
	first, err := q.AcquireChannelIngressLease(ctx, sqlc.AcquireChannelIngressLeaseParams{
		OwnerID:    "owner-1",
		TtlSeconds: 60,
	})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first.OwnerID != "owner-1" {
		t.Fatalf("first acquire owner = %q, want owner-1", first.OwnerID)
	}

	// Second acquirer must get NO rows (pgx.ErrNoRows) while the lease is live.
	if _, err := q.AcquireChannelIngressLease(ctx, sqlc.AcquireChannelIngressLeaseParams{
		OwnerID:    "owner-2",
		TtlSeconds: 60,
	}); err == nil {
		t.Fatal("second acquirer got the lease while a live lease was held")
	}

	// The holder can renew (idempotent self-acquire).
	if _, err := q.AcquireChannelIngressLease(ctx, sqlc.AcquireChannelIngressLeaseParams{
		OwnerID:    "owner-1",
		TtlSeconds: 60,
	}); err != nil {
		t.Fatalf("holder renew: %v", err)
	}

	// After the holder releases, the second acquirer succeeds.
	if _, err := q.ReleaseChannelIngressLease(ctx, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := q.AcquireChannelIngressLease(ctx, sqlc.AcquireChannelIngressLeaseParams{
		OwnerID:    "owner-2",
		TtlSeconds: 60,
	})
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	if second.OwnerID != "owner-2" {
		t.Fatalf("post-release owner = %q, want owner-2", second.OwnerID)
	}
}
