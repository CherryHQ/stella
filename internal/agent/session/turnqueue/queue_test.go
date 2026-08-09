package turnqueue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueFIFOAndNoConcurrentExecution(t *testing.T) {
	q := NewWithLimits(8, time.Second, time.Second)
	block := make(chan struct{})
	firstStarted := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	var mu sync.Mutex
	order := make([]int, 0, 3)

	run := func(id int, gate <-chan struct{}, started chan<- struct{}) func(context.Context, context.Context, func() error) error {
		return func(_ context.Context, runCtx context.Context, beforeStart func() error) error {
			if err := beforeStart(); err != nil {
				return err
			}
			current := active.Add(1)
			defer active.Add(-1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) {
					break
				}
			}
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			if started != nil {
				close(started)
			}
			if gate != nil {
				select {
				case <-gate:
				case <-runCtx.Done():
					return runCtx.Err()
				}
			}
			return nil
		}
	}

	errC := make(chan error, 3)
	go func() { errC <- q.Enqueue(context.Background(), "session", run(1, block, firstStarted)) }()
	<-firstStarted
	go func() { errC <- q.Enqueue(context.Background(), "session", run(2, nil, nil)) }()
	waitQueueDepth(t, q, "session", 1)
	go func() { errC <- q.Enqueue(context.Background(), "session", run(3, nil, nil)) }()
	waitQueueDepth(t, q, "session", 2)
	close(block)
	for range 3 {
		if err := <-errC; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got := order; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("execution order = %v, want [1 2 3]", got)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent executions = %d, want 1", got)
	}
}

func TestQueueDepthOverflow(t *testing.T) {
	q := NewWithLimits(1, time.Second, time.Second)
	block := make(chan struct{})
	started := make(chan struct{})
	first := func(_ context.Context, _ context.Context, beforeStart func() error) error {
		if err := beforeStart(); err != nil {
			return err
		}
		close(started)
		<-block
		return nil
	}
	go func() { _ = q.Enqueue(context.Background(), "session", first) }()
	<-started
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- q.Enqueue(context.Background(), "session", func(_ context.Context, _ context.Context, beforeStart func() error) error {
			return beforeStart()
		})
	}()
	waitQueueDepth(t, q, "session", 1)
	if err := q.Enqueue(context.Background(), "session", func(context.Context, context.Context, func() error) error { return nil }); !errors.Is(err, ErrFull) {
		t.Fatalf("overflow error = %v, want ErrFull", err)
	}
	close(block)
	if err := <-waiterDone; err != nil {
		t.Fatal(err)
	}
}

func TestQueueTimeoutNeverStartsQueuedTurn(t *testing.T) {
	q := NewWithLimits(2, 30*time.Millisecond, time.Second)
	block := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = q.Enqueue(context.Background(), "session", func(_ context.Context, _ context.Context, beforeStart func() error) error {
			if err := beforeStart(); err != nil {
				return err
			}
			close(started)
			<-block
			return nil
		})
	}()
	<-started
	var ran atomic.Bool
	err := q.Enqueue(context.Background(), "session", func(_ context.Context, _ context.Context, beforeStart func() error) error {
		if err := beforeStart(); err != nil {
			return err
		}
		ran.Store(true)
		return nil
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	close(block)
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("timed-out queued turn started")
	}
}

func TestQueueSourceCancellationQueuedAndHeld(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		q := NewWithLimits(2, time.Second, time.Second)
		block := make(chan struct{})
		started := make(chan struct{})
		go func() {
			_ = q.Enqueue(context.Background(), "session", func(_ context.Context, _ context.Context, beforeStart func() error) error {
				if err := beforeStart(); err != nil {
					return err
				}
				close(started)
				<-block
				return nil
			})
		}()
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		var ran atomic.Bool
		errC := make(chan error, 1)
		go func() {
			errC <- q.Enqueue(ctx, "session", func(_ context.Context, _ context.Context, beforeStart func() error) error {
				if err := beforeStart(); err != nil {
					return err
				}
				ran.Store(true)
				return nil
			})
		}()
		waitQueueDepth(t, q, "session", 1)
		cancel()
		if err := <-errC; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		close(block)
		time.Sleep(20 * time.Millisecond)
		if ran.Load() {
			t.Fatal("canceled queued turn started")
		}
	})

	t.Run("held", func(t *testing.T) {
		q := NewWithLimits(2, time.Second, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		admitted := make(chan struct{})
		errC := make(chan error, 1)
		go func() {
			errC <- q.Enqueue(ctx, "session", func(_ context.Context, runCtx context.Context, beforeStart func() error) error {
				if err := beforeStart(); err != nil {
					return err
				}
				close(admitted)
				<-runCtx.Done()
				return runCtx.Err()
			})
		}()
		<-admitted
		cancel()
		if err := <-errC; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		// The next turn proves the canceled holder released the FIFO slot.
		if err := q.Enqueue(context.Background(), "session", func(_ context.Context, _ context.Context, beforeStart func() error) error { return beforeStart() }); err != nil {
			t.Fatalf("next turn: %v", err)
		}
	})
}

func waitQueueDepth(t *testing.T, q *Queue, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		s := q.sessions[key]
		got := 0
		if s != nil {
			got = len(s.queue)
		}
		q.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue depth did not reach %d", want)
}
