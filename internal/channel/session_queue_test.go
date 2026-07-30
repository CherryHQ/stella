package channel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// makeStream builds a ChatStream whose Events channel emits the provided events
// then closes. It returns the stream and a function to unblock the drain after
// the test has had a chance to inspect ordering.
func makeStream(events ...pkgchannel.Event) *pkgchannel.ChatStream {
	ch := make(chan pkgchannel.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return &pkgchannel.ChatStream{Events: ch, SessionID: "test-session"}
}

// TestSessionQueue_SerializesSameSession verifies that two requests for the
// same session are processed strictly in order (second starts only after first
// stream is drained).
func TestSessionQueue_SerializesSameSession(t *testing.T) {
	q := newSessionQueue()
	ctx := context.Background()

	var order []int
	var orderMu sync.Mutex
	record := func(n int) { orderMu.Lock(); order = append(order, n); orderMu.Unlock() }

	firstRunning := make(chan struct{})
	firstUnblock := make(chan struct{})

	// First fn: signals it is running, then blocks until test unblocks it.
	fn1 := func(ctx context.Context) (*pkgchannel.ChatStream, error) {
		record(1)
		close(firstRunning)
		<-firstUnblock
		return makeStream(pkgchannel.Event{Text: "a"}), nil
	}
	fn2 := func(ctx context.Context) (*pkgchannel.ChatStream, error) {
		record(2)
		return makeStream(pkgchannel.Event{Text: "b"}), nil
	}

	// Enqueue fn1 in background.
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		stream, doneC, err := q.Enqueue(ctx, "sess-a", fn1)
		if err != nil {
			t.Errorf("fn1 enqueue error: %v", err)
			return
		}
		for range stream.Events {
		}
		close(doneC)
	}()

	// Wait until fn1 is running before enqueuing fn2.
	<-firstRunning

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		stream, doneC, err := q.Enqueue(ctx, "sess-a", fn2)
		if err != nil {
			t.Errorf("fn2 enqueue error: %v", err)
			return
		}
		for range stream.Events {
		}
		close(doneC)
	}()

	// Unblock fn1.
	close(firstUnblock)
	<-done1
	<-done2

	orderMu.Lock()
	defer orderMu.Unlock()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Errorf("expected order [1 2], got %v", order)
	}
}

// TestSessionQueue_ParallelDifferentSessions verifies that different sessions
// are dispatched concurrently.
func TestSessionQueue_ParallelDifferentSessions(t *testing.T) {
	q := newSessionQueue()
	ctx := context.Background()

	gate := make(chan struct{})
	var running atomic.Int32

	makeFn := func() func(context.Context) (*pkgchannel.ChatStream, error) {
		return func(ctx context.Context) (*pkgchannel.ChatStream, error) {
			<-gate
			running.Add(1)
			return makeStream(), nil
		}
	}

	done1 := make(chan struct{})
	done2 := make(chan struct{})

	go func() {
		defer close(done1)
		stream, doneC, _ := q.Enqueue(ctx, "sess-x", makeFn())
		if stream != nil {
			for range stream.Events {
			}
			close(doneC)
		}
	}()
	go func() {
		defer close(done2)
		stream, doneC, _ := q.Enqueue(ctx, "sess-y", makeFn())
		if stream != nil {
			for range stream.Events {
			}
			close(doneC)
		}
	}()

	// Give goroutines time to reach the gate.
	time.Sleep(20 * time.Millisecond)

	// Release both at once.
	close(gate)
	<-done1
	<-done2

	if n := running.Load(); n != 2 {
		t.Errorf("expected 2 concurrent runs, got %d", n)
	}
}

// TestSessionQueue_AbortActive verifies that Abort cancels the running request.
func TestSessionQueue_AbortActive(t *testing.T) {
	q := newSessionQueue()
	ctx := context.Background()

	started := make(chan struct{})
	fn := func(ctx context.Context) (*pkgchannel.ChatStream, error) {
		close(started)
		// Block until context is cancelled.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	done := make(chan struct{})
	var gotErr error
	go func() {
		defer close(done)
		_, _, err := q.Enqueue(ctx, "sess-abort", fn)
		gotErr = err
	}()

	<-started
	if !q.Abort("sess-abort") {
		t.Error("Abort returned false, expected true")
	}
	<-done

	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", gotErr)
	}
}

// TestSessionQueue_AbortNoActive verifies that Abort returns false when no
// request is running.
func TestSessionQueue_AbortNoActive(t *testing.T) {
	q := newSessionQueue()
	if q.Abort("nonexistent") {
		t.Error("Abort returned true for nonexistent session")
	}
}

// TestSessionQueue_AbortQueued verifies that an aborted-then-completed first
// request allows the second queued request to run normally.
func TestSessionQueue_AbortQueued(t *testing.T) {
	q := newSessionQueue()
	ctx := context.Background()

	started1 := make(chan struct{})
	fn1 := func(ctx context.Context) (*pkgchannel.ChatStream, error) {
		close(started1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	fn2 := func(ctx context.Context) (*pkgchannel.ChatStream, error) {
		return makeStream(pkgchannel.Event{Text: "queued-ran"}), nil
	}

	var result2 string

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_, _, _ = q.Enqueue(ctx, "sess-q", fn1) // will error
	}()

	<-started1

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		stream, doneC, err := q.Enqueue(ctx, "sess-q", fn2)
		if err != nil {
			return
		}
		for evt := range stream.Events {
			result2 += evt.Text
		}
		close(doneC)
	}()

	q.Abort("sess-q")
	<-done1
	<-done2

	if result2 != "queued-ran" {
		t.Errorf("expected queued-ran from fn2, got %q", result2)
	}
}

// TestSessionQueue_CallerContextCancelled verifies that if the caller's context
// is cancelled while waiting in the queue, Enqueue returns immediately.
func TestSessionQueue_CallerContextCancelled(t *testing.T) {
	q := newSessionQueue()

	// Block slot with a long-running fn.
	blockCtx := context.Background()
	unblock := make(chan struct{})
	started := make(chan struct{})
	go func() {
		q.Enqueue(blockCtx, "sess-cc", func(ctx context.Context) (*pkgchannel.ChatStream, error) { //nolint:errcheck
			close(started)
			<-unblock
			return makeStream(), nil
		})
	}()
	<-started

	// Enqueue a second request with a cancellable context.
	callerCtx, callerCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var gotErr error
	go func() {
		defer close(done)
		_, _, gotErr = q.Enqueue(callerCtx, "sess-cc", func(ctx context.Context) (*pkgchannel.ChatStream, error) {
			return makeStream(), nil
		})
	}()

	// Cancel the caller before fn1 finishes.
	callerCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue did not return after caller context was cancelled")
	}

	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", gotErr)
	}

	close(unblock)
}

func waitForSessionRemoval(t *testing.T, q *sessionQueue, sessionKey string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		_, ok := q.sessions[sessionKey]
		q.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("session %q was not reclaimed after becoming idle", sessionKey)
}

func TestSessionQueue_ReclaimsIdleSlot(t *testing.T) {
	q := newSessionQueueWithIdleTimeout(20 * time.Millisecond)

	stream, doneC, err := q.Enqueue(context.Background(), "sess-idle", func(ctx context.Context) (*pkgchannel.ChatStream, error) {
		return makeStream(pkgchannel.Event{Text: "done"}), nil
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	for range stream.Events {
	}
	close(doneC)

	waitForSessionRemoval(t, q, "sess-idle")
}

func TestSessionQueue_RecreatesSlotAfterIdleCleanup(t *testing.T) {
	q := newSessionQueueWithIdleTimeout(20 * time.Millisecond)

	runOnce := func(sessionKey string) error {
		stream, doneC, err := q.Enqueue(context.Background(), sessionKey, func(ctx context.Context) (*pkgchannel.ChatStream, error) {
			return makeStream(pkgchannel.Event{Text: "ok"}), nil
		})
		if err != nil {
			return err
		}
		for range stream.Events {
		}
		close(doneC)
		return nil
	}

	if err := runOnce("sess-recreate"); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}

	waitForSessionRemoval(t, q, "sess-recreate")

	if err := runOnce("sess-recreate"); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
}

// TestSessionQueue_ControlOpReleasesSlotWhenCallerGivesUp covers the abandoned
// control operation: it produces no stream, so the queue must still close its
// done channel or the session slot stays blocked forever.
func TestSessionQueue_ControlOpReleasesSlotWhenCallerGivesUp(t *testing.T) {
	q := newSessionQueue()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()

	err := q.EnqueueControl(ctx, "sess-ctl", func(opCtx context.Context) error {
		close(started)
		<-opCtx.Done()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnqueueControl error = %v, want context.Canceled", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stream, doneC, err := q.Enqueue(context.Background(), "sess-ctl", func(context.Context) (*pkgchannel.ChatStream, error) {
			return makeStream(pkgchannel.Event{Text: "next"}), nil
		})
		if err != nil {
			t.Errorf("follow-up enqueue: %v", err)
			return
		}
		for range stream.Events {
		}
		close(doneC)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session slot stayed blocked after an abandoned control operation")
	}
}
