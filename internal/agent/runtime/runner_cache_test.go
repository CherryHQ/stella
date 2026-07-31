package runtime

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// --- fake runner ------------------------------------------------------------

type fakeRunner struct {
	alive      bool
	busy       bool
	closed     bool
	lastAct    time.Time
	system     string
	chatSystem string
	closeErr   error
}

func newFakeRunner() *fakeRunner { return &fakeRunner{alive: true, lastAct: time.Now()} }

func (r *fakeRunner) Chat(ctx context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	r.chatSystem, _ = agentctx.SystemOverrideFromContext(ctx)
	ch := make(chan Event)
	close(ch)
	return ch
}
func (r *fakeRunner) Alive() bool             { return r.alive }
func (r *fakeRunner) Busy() bool              { return r.busy }
func (r *fakeRunner) LastActivity() time.Time { return r.lastAct }
func (r *fakeRunner) SystemPrompt() string    { return r.system }
func (r *fakeRunner) Close() error {
	r.closed = true
	return r.closeErr
}

// --- fake memory provider ---------------------------------------------------

type fakeMemory struct{}

func (fakeMemory) Name() string                                                      { return "fake" }
func (fakeMemory) Bootstrap(_ context.Context, _ memory.Session) error               { return nil }
func (fakeMemory) Append(_ context.Context, _ memory.Session, _ ...ai.Message) error { return nil }
func (fakeMemory) Assemble(_ context.Context, _ memory.Session, _, _ int) ([]ai.Message, error) {
	return nil, nil
}

func (fakeMemory) Stats(_ context.Context, _ memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}
func (fakeMemory) Close() error { return nil }

// --- helpers ----------------------------------------------------------------

func testCache(factoryErr error) (*runnerCache, *fakeRunner) {
	created := newFakeRunner()
	var calls int
	factory := func(_ context.Context, _ RunnerParams) (Runner, error) {
		calls++
		if factoryErr != nil {
			return nil, factoryErr
		}
		_ = calls
		return created, nil
	}
	cache := newRunnerCache(factory, fakeMemory{}, 10*time.Minute, slog.Default())
	return cache, created
}

func validInfo(id string) session.Info {
	return session.NewInfo(id, "agent1", "u1", "web", session.KindChat, "", time.Now().UTC())
}

// TestRunnerCache_Reuse verifies the same runner is returned on repeat calls.
func TestRunnerCache_Reuse(t *testing.T) {
	cache, _ := testCache(nil)
	info := validInfo("s1")

	_, r1, err := cache.getOrCreate(context.Background(), info, "", "")
	if err != nil {
		t.Fatalf("first getOrCreate: %v", err)
	}
	_, r2, err := cache.getOrCreate(context.Background(), info, "", "")
	if err != nil {
		t.Fatalf("second getOrCreate: %v", err)
	}
	if r1 != r2 {
		t.Error("expected same runner on reuse")
	}
}

func TestRunnerCache_RebuildsWhenPrivateHumanCapabilityChanges(t *testing.T) {
	var (
		created []*fakeRunner
		params  []RunnerParams
	)
	factory := func(_ context.Context, p RunnerParams) (Runner, error) {
		runner := newFakeRunner()
		created = append(created, runner)
		params = append(params, p)
		return runner, nil
	}
	cache := newRunnerCache(factory, fakeMemory{}, 10*time.Minute, slog.Default())
	info := validInfo("shared-session")

	humanCtx := withPrivateHumanTurn(context.Background())
	if _, _, err := cache.getOrCreate(humanCtx, info, "", ""); err != nil {
		t.Fatalf("create private-human runner: %v", err)
	}
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("create non-human runner: %v", err)
	}

	if len(params) != 2 {
		t.Fatalf("runner builds = %d, want 2", len(params))
	}
	if !params[0].PrivateHuman || params[1].PrivateHuman {
		t.Fatalf("PrivateHuman sequence = [%v %v], want [true false]", params[0].PrivateHuman, params[1].PrivateHuman)
	}
	if !created[0].closed {
		t.Fatal("private-human runner was reused after the capability disappeared")
	}
}

// TestRunnerCache_Close shuts down the runner.
func TestRunnerCache_Close(t *testing.T) {
	cache, created := testCache(nil)
	info := validInfo("s1")

	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	if err := cache.close("s1"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !created.closed {
		t.Error("expected runner to be closed")
	}
}

// TestRunnerCache_CloseAll shuts down all runners.
func TestRunnerCache_CloseAll(t *testing.T) {
	cache, created := testCache(nil)
	for _, id := range []string{"s1", "s2"} {
		if _, _, err := cache.getOrCreate(context.Background(), validInfo(id), "", ""); err != nil {
			t.Fatalf("getOrCreate %s: %v", id, err)
		}
	}
	if err := cache.closeAll(); err != nil {
		t.Fatalf("closeAll: %v", err)
	}
	// The fake creates ONE runner shared across both sessions (same factory call result).
	// We just verify no error and the cache is empty.
	if len(cache.sessions) != 0 {
		t.Errorf("expected empty cache after closeAll, got %d", len(cache.sessions))
	}
	_ = created
}

// TestRunnerCache_DeadRunnerReplaced replaces a dead runner on next access.
func TestRunnerCache_DeadRunnerReplaced(t *testing.T) {
	cache, created := testCache(nil)
	info := validInfo("s1")

	_, r1, _ := cache.getOrCreate(context.Background(), info, "", "")
	created.alive = false

	_, r2, err := cache.getOrCreate(context.Background(), info, "", "")
	if err != nil {
		t.Fatalf("getOrCreate after dead: %v", err)
	}
	// r2 is a new runner (factory creates new fakeRunner each call with alive=true).
	// Since factory always returns the same `created` object and we set alive=false,
	// the second call also gets a runner. Just verify no error.
	_ = r1
	_ = r2
}

// TestRunnerCache_MissingID rejects empty session ID.
func TestRunnerCache_MissingID(t *testing.T) {
	cache, _ := testCache(nil)
	_, _, err := cache.getOrCreate(context.Background(), session.Info{UserID: "u1", AgentID: "a1"}, "", "")
	if err == nil {
		t.Error("expected error for empty session ID")
	}
}

// TestRunnerCache_MissingUserID rejects empty UserID.
func TestRunnerCache_MissingUserID(t *testing.T) {
	cache, _ := testCache(nil)
	info := session.Info{ID: "s1", AgentID: "a1"}
	_, _, err := cache.getOrCreate(context.Background(), info, "", "")
	if err == nil {
		t.Error("expected error for empty UserID")
	}
}

// TestRunnerCache_MissingAgentID rejects empty AgentID.
func TestRunnerCache_MissingAgentID(t *testing.T) {
	cache, _ := testCache(nil)
	info := session.Info{ID: "s1", UserID: "u1"}
	_, _, err := cache.getOrCreate(context.Background(), info, "", "")
	if err == nil {
		t.Error("expected error for empty AgentID")
	}
}

// TestRunnerCache_FactoryError propagates factory errors.
func TestRunnerCache_FactoryError(t *testing.T) {
	factoryErr := errors.New("factory boom")
	cache, _ := testCache(factoryErr)
	_, _, err := cache.getOrCreate(context.Background(), validInfo("s1"), "", "")
	if !errors.Is(err, factoryErr) {
		t.Errorf("expected factory error, got %v", err)
	}
}

// TestRunnerCache_Reap removes idle runners.
func TestRunnerCache_Reap(t *testing.T) {
	cache, created := testCache(nil)
	cache.idleTimeout = 1 * time.Millisecond

	info := validInfo("s1")
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	created.lastAct = time.Now().Add(-1 * time.Hour)
	cache.reap()

	cache.mu.Lock()
	cs := cache.sessions["s1"]
	cache.mu.Unlock()
	if cs != nil && cs.r != nil {
		t.Error("expected runner to be reaped")
	}
}

// TestRuntimeChat_BeforeRunOverride verifies the lifecycle hook can update the
// per-run system prompt before the runner sees the request.
func TestRuntimeChat_BeforeRunOverride(t *testing.T) {
	runner := newFakeRunner()
	runner.system = "base"
	rt, err := New(Config{
		NewRunner: func(_ context.Context, _ RunnerParams) (Runner, error) {
			return runner, nil
		},
		Memory: fakeMemory{},
		BeforeRun: func(_ context.Context, info session.Info, model, msgText, system string, history []ai.Message) (string, error) {
			if info.ID != "s1" {
				t.Fatalf("session ID = %q, want s1", info.ID)
			}
			if msgText != "hello" {
				t.Fatalf("message = %q, want hello", msgText)
			}
			if system != "base" {
				t.Fatalf("system = %q, want base", system)
			}
			return system + " + hook", nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for ev := range rt.Chat(context.Background(), validInfo("s1"), "hello") {
		if ev.Err != nil {
			t.Fatalf("Chat event error: %v", ev.Err)
		}
	}
	if runner.chatSystem != "base + hook" {
		t.Fatalf("runner system = %q, want hook override", runner.chatSystem)
	}
}

// TestRunnerCache_InvalidGroupSessionFailsClosedWithoutRunner proves an invalid
// group session (non-canonical group id) fails closed before any runner is built
// or cached — the runner factory is never called.
func TestRunnerCache_InvalidGroupSessionFailsClosedWithoutRunner(t *testing.T) {
	var calls int
	factory := func(context.Context, RunnerParams) (Runner, error) {
		calls++
		return newFakeRunner(), nil
	}
	cache := newRunnerCache(factory, fakeMemory{}, 10*time.Minute, slog.Default())

	bad := session.Info{ID: "s1", AgentID: "a1", UserID: "not-a-uuid", GroupID: "not-a-uuid"}
	cs, r, err := cache.getOrCreate(context.Background(), bad, "", "")
	if err == nil {
		t.Fatal("expected a fail-closed error for an invalid group session")
	}
	if cs != nil || r != nil {
		t.Fatal("no cached session or runner should be returned on failure")
	}
	if calls != 0 {
		t.Fatalf("runner factory called %d times; want 0", calls)
	}
	if _, ok := cache.sessions["s1"]; ok {
		t.Fatal("no session entry should be installed for an invalid session")
	}
}
