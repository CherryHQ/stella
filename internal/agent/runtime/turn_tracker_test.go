package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestTurnTrackerWaitIdleImmediately proves wait returns at once when nothing
// is in flight — the common shutdown case must not block.
func TestTurnTrackerWaitIdleImmediately(t *testing.T) {
	var tr turnTracker
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tr.wait(ctx); err != nil {
		t.Fatalf("wait on idle tracker = %v, want nil", err)
	}
}

// TestTurnTrackerWaitBlocksUntilEnd proves wait blocks while a turn is in
// flight and unblocks when the last one ends.
func TestTurnTrackerWaitBlocksUntilEnd(t *testing.T) {
	var tr turnTracker
	tr.begin()
	tr.begin()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- tr.wait(ctx)
	}()

	tr.end()
	select {
	case err := <-done:
		t.Fatalf("wait returned (%v) with one turn still in flight", err)
	case <-time.After(50 * time.Millisecond):
	}

	tr.end()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait after last end = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not unblock after the last turn ended")
	}
}

// TestTurnTrackerWaitBounded proves wait honors its context: an in-flight turn
// that never ends must not hang the caller past its deadline.
func TestTurnTrackerWaitBounded(t *testing.T) {
	var tr turnTracker
	tr.begin()
	defer tr.end()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := tr.wait(ctx); err == nil {
		t.Fatal("wait = nil with a turn still in flight, want context error")
	}
}

// TestTurnTrackerReuseAfterIdle proves the tracker survives idle→busy cycles:
// a turn beginning after a previous idle close must block wait again (the idle
// channel is replaced, not reused after close).
func TestTurnTrackerReuseAfterIdle(t *testing.T) {
	var tr turnTracker
	tr.begin()
	tr.end()

	tr.begin()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := tr.wait(ctx); err == nil {
		t.Fatal("wait = nil after idle→busy cycle, want context error (stale idle channel)")
	}
	tr.end()

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := tr.wait(ctx2); err != nil {
		t.Fatalf("wait after final end = %v, want nil", err)
	}
}

// TestTurnTrackerConcurrent hammers begin/end from many goroutines and proves
// a bounded wait afterwards observes idle — no lost end leaves the counter
// stuck positive.
func TestTurnTrackerConcurrent(t *testing.T) {
	var tr turnTracker
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for range 20 {
				tr.begin()
				tr.end()
			}
		})
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tr.wait(ctx); err != nil {
		t.Fatalf("wait after concurrent churn = %v, want nil", err)
	}
}
