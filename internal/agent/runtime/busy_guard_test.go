package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/ai"
)

type blockingRunner struct {
	gate chan struct{}
}

func (r *blockingRunner) Chat(_ context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	out := make(chan Event)
	go func() {
		<-r.gate
		close(out)
	}()
	return out
}
func (r *blockingRunner) Alive() bool             { return true }
func (r *blockingRunner) Busy() bool              { return false }
func (r *blockingRunner) LastActivity() time.Time { return time.Now() }
func (r *blockingRunner) SystemPrompt() string    { return "" }
func (r *blockingRunner) Close() error            { return nil }

func newTestRuntime(gate chan struct{}) *Runtime {
	mem := &recordingMemory{}
	rt, _ := New(Config{
		NewRunner: func(_ context.Context, _ RunnerParams) (Runner, error) {
			return &blockingRunner{gate: gate}, nil
		},
		Memory: mem,
	})
	return rt
}

func waitSessionFree(t *testing.T, rt *Runtime, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := rt.active.Load(sessionID); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session %s did not become free", sessionID)
}

func TestChat_BusyGuard_RejectsConcurrentSameSession(t *testing.T) {
	gate := make(chan struct{})
	rt := newTestRuntime(gate)

	info := session.Info{
		ID:      "sess-1",
		UserID:  "u1",
		AgentID: "a1",
	}

	ch1 := rt.Chat(context.Background(), info, "hello")

	// Second chat on same session should be rejected immediately.
	ch2 := rt.Chat(context.Background(), info, "world")
	evt := <-ch2
	if evt.Err == nil || !errors.Is(evt.Err, ErrSessionBusy) {
		t.Fatalf("expected ErrSessionBusy, got %v", evt.Err)
	}

	// Release the first chat and drain. The output channel closes before the
	// outer Chat goroutine runs its active-session cleanup, so wait for the guard
	// itself to clear instead of racing the cleanup defer.
	close(gate)
	for range ch1 {
	}
	waitSessionFree(t, rt, info.ID)

	// After first completes, the session should be free again.
	gate2 := make(chan struct{})
	rt.cache.mu.Lock()
	for _, cs := range rt.cache.sessions {
		cs.r = &blockingRunner{gate: gate2}
	}
	rt.cache.mu.Unlock()

	ch3 := rt.Chat(context.Background(), info, "retry")
	// Give goroutine time to start.
	time.Sleep(10 * time.Millisecond)
	// Should not be busy — it should be running, not rejected.
	select {
	case evt := <-ch3:
		if evt.Err != nil && errors.Is(evt.Err, ErrSessionBusy) {
			t.Fatal("session should be free after first turn completes")
		}
	default:
		// Expected: ch3 is blocking because runner is active.
	}
	close(gate2)
	for range ch3 {
	}
}

func TestChatAdmitted_BusyGuardReturnsSynchronousRejection(t *testing.T) {
	gate := make(chan struct{})
	rt := newTestRuntime(gate)
	info := session.Info{ID: "sess-admitted", UserID: "u1", AgentID: "a1"}
	first, err := rt.ChatAdmitted(context.Background(), info, "first")
	if err != nil || first == nil {
		t.Fatalf("first admission = %v, %v", first, err)
	}
	second, err := rt.ChatAdmitted(context.Background(), info, "second")
	if second != nil || !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second admission = %v, %v; want nil, ErrSessionBusy", second, err)
	}
	close(gate)
	for range first {
	}
}

func TestChat_BusyGuard_AllowsDifferentSessions(t *testing.T) {
	gate := make(chan struct{})
	rt := newTestRuntime(gate)

	info1 := session.Info{ID: "sess-1", UserID: "u1", AgentID: "a1"}
	info2 := session.Info{ID: "sess-2", UserID: "u1", AgentID: "a1"}

	ch1 := rt.Chat(context.Background(), info1, "hello")
	ch2 := rt.Chat(context.Background(), info2, "world")

	// Neither should be rejected.
	time.Sleep(10 * time.Millisecond)
	select {
	case evt := <-ch2:
		if evt.Err != nil && errors.Is(evt.Err, ErrSessionBusy) {
			t.Fatal("different session should not be rejected")
		}
	default:
		// Expected: ch2 is running (not rejected).
	}

	close(gate)
	for range ch1 {
	}
	for range ch2 {
	}
}
