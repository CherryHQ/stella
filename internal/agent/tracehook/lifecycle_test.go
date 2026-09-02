package tracehook

// Issue #708 Section D: the constructor starts no goroutine; the reaper loop is
// activated by an explicit ctx-bound Start and exits on ctx cancellation
// (no goroutine leak).

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// settleAtOrBelow polls until the live goroutine count is <= target, or fails.
func settleAtOrBelow(t *testing.T, target int, what string) {
	t.Helper()
	for range 400 {
		if runtime.NumGoroutine() <= target {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: goroutine count %d never settled to <= %d", what, runtime.NumGoroutine(), target)
}

func TestConstructorNoGoroutineStartActivatesReaperCancelStops(t *testing.T) {
	base := runtime.NumGoroutine()

	h := New(true, false)
	// Constructor must not have started the reaper.
	settleAtOrBelow(t, base, "after New")

	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)

	// Start must have launched the reaper goroutine.
	started := false
	for range 400 {
		if runtime.NumGoroutine() > base {
			started = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !started {
		t.Fatal("Start did not launch the reaper goroutine")
	}

	// Cancelling the context must stop the reaper — no goroutine leak.
	cancel()
	settleAtOrBelow(t, base, "after ctx cancel")
}

func TestStartIsNoOpWhenDisabledAndIdempotent(t *testing.T) {
	base := runtime.NumGoroutine()

	// Disabled hook: Start launches nothing.
	off := New(false, false)
	off.Start(context.Background())
	settleAtOrBelow(t, base, "disabled Start")

	// Enabled hook: repeated Start launches at most one reaper (sync.Once).
	ctx := t.Context()
	on := New(true, false)
	on.Start(ctx)
	on.Start(ctx)
	on.Start(ctx)
	settleAtOrBelow(t, base+1, "idempotent Start (at most one reaper)")
}
