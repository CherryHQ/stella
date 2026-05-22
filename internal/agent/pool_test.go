package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	lcmmemory "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// testMemoryProvider creates an LCM memory provider backed by an in-memory SQLite DB.
func testMemoryProvider(t *testing.T) memory.Provider {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	p, err := lcmmemory.New(db, nil, nil)
	if err != nil {
		t.Fatalf("Build lcm provider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// mockRunner implements Runner for pool tests.
type mockRunner struct {
	mu           sync.Mutex
	events       []Event
	system       string
	seenOverride string
	closed       bool
	lastActivity time.Time
	alive        bool
}

func newMockRunner(events []Event) *mockRunner {
	return &mockRunner{
		events:       events,
		system:       "base system prompt",
		lastActivity: time.Now(),
		alive:        true,
	}
}

func testSessionContext() context.Context {
	return memory.WithAgentID(memory.WithUserID(context.Background(), "test-user"), "test-agent")
}

func (m *mockRunner) Chat(ctx context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	m.mu.Lock()
	m.lastActivity = time.Now()
	if override, ok := SystemOverrideFromContext(ctx); ok {
		m.seenOverride = override
	}
	events := m.events
	m.mu.Unlock()

	out := make(chan Event, len(events))
	go func() {
		defer close(out)
		for _, evt := range events {
			out <- evt
		}
	}()
	return out
}

func (m *mockRunner) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.alive = false
	return nil
}

func (m *mockRunner) Alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive
}

func (m *mockRunner) LastActivity() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastActivity
}

func (m *mockRunner) Busy() bool { return false }

func (m *mockRunner) SystemPrompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.system
}

// mockRunnerFactory returns a NewRunnerFunc that creates mockRunners with the
// given canned events. It also tracks all created runners.
func mockRunnerFactory(events []Event) (NewRunnerFunc, *[]*mockRunner) {
	var runners []*mockRunner
	var mu sync.Mutex
	factory := func(_ context.Context, _ RunnerParams) (Runner, error) {
		r := newMockRunner(events)
		mu.Lock()
		runners = append(runners, r)
		mu.Unlock()
		return r, nil
	}
	return factory, &runners
}

func TestNewPool(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithIdleTimeout(5*time.Minute))

	if pool.idleTimeout != 5*time.Minute {
		t.Errorf("idleTimeout = %v, want 5m", pool.idleTimeout)
	}
}

func TestWithCompaction(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	cfg := CompactionConfig{MaxTokens: 40_000, KeepTail: 10}
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithCompaction(cfg))

	if pool.compaction.MaxTokens != 40_000 {
		t.Errorf("MaxTokens = %d, want 40000", pool.compaction.MaxTokens)
	}
	if pool.compaction.KeepTail != 10 {
		t.Errorf("KeepTail = %d, want 10", pool.compaction.KeepTail)
	}
}

func TestSetFactory(t *testing.T) {
	factory1, _ := mockRunnerFactory(nil)
	pool := NewPool(factory1, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	factory2, _ := mockRunnerFactory([]Event{{Text: "new"}})
	pool.SetFactory(factory2)

	// Verify factory was replaced by creating a session
	ctx := testSessionContext()
	info, _ := pool.CreateSession("test", "test-user")
	stream := pool.Chat(ctx, info.ID, "hi")
	var got string
	for evt := range stream {
		got += evt.Text
	}
	if got != "new" {
		t.Errorf("got %q, want %q from new factory", got, "new")
	}
}

func TestPoolChat(t *testing.T) {
	events := []Event{
		{Text: "Hello "},
		{Text: "world"},
	}
	factory, _ := mockRunnerFactory(events)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()
	stream := pool.Chat(ctx, "session-1", "test")

	var collected string
	for evt := range stream {
		if evt.Err != nil {
			t.Fatalf("unexpected error: %v", evt.Err)
		}
		collected += evt.Text
	}

	if collected != "Hello world" {
		t.Errorf("collected = %q, want %q", collected, "Hello world")
	}
}

func TestPoolChatAppliesBeforeRunSystemOverride(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithBeforeRunBuilder(func(context.Context, pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
		return pkgplugins.BeforeRunResult{SystemPrompt: "override system prompt"}, nil
	}))
	defer func() { _ = pool.Close() }()

	stream := pool.Chat(testSessionContext(), "session-1", "test")
	for evt := range stream {
		if evt.Err != nil {
			t.Fatalf("unexpected error: %v", evt.Err)
		}
	}

	if len(*runners) != 1 {
		t.Fatalf("expected 1 runner created, got %d", len(*runners))
	}

	(*runners)[0].mu.Lock()
	defer (*runners)[0].mu.Unlock()
	if (*runners)[0].seenOverride != "override system prompt" {
		t.Fatalf("seen override = %q, want %q", (*runners)[0].seenOverride, "override system prompt")
	}
}

func TestPoolChatReusesSession(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	// First chat creates a
	stream := pool.Chat(ctx, "sess-1", "first")
	for range stream {
	}

	// Second chat should reuse the same
	stream = pool.Chat(ctx, "sess-1", "second")
	for range stream {
	}

	if len(*runners) != 1 {
		t.Errorf("expected 1 runner created, got %d", len(*runners))
	}
}

func TestPoolChatMultipleSessions(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	stream := pool.Chat(ctx, "a", "msg")
	for range stream {
	}

	stream = pool.Chat(ctx, "b", "msg")
	for range stream {
	}

	if len(*runners) != 2 {
		t.Errorf("expected 2 runners created, got %d", len(*runners))
	}
}

func TestPoolChatAccumulatesHistory(t *testing.T) {
	events := []Event{
		{Text: "chunk1"},
		{Text: "chunk2"},
	}
	factory, _ := mockRunnerFactory(events)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	stream := pool.Chat(ctx, "sess", "msg")
	for range stream {
	}

	// Verify history via pool.History() (memory engine).
	history := pool.History("sess")
	// 1 user_message + 1 assembled assistant message = 2 events.
	if len(history) != 2 {
		t.Errorf("history length = %d, want 2", len(history))
	}
}

func TestPoolChatErrorFromFactory(t *testing.T) {
	factory := func(_ context.Context, _ RunnerParams) (Runner, error) {
		return nil, fmt.Errorf("factory error")
	}
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	stream := pool.Chat(testSessionContext(), "sess", "msg")

	var gotErr error
	for evt := range stream {
		if evt.Err != nil {
			gotErr = evt.Err
			break
		}
	}

	if gotErr == nil {
		t.Fatal("expected error from factory")
	}
}

func TestPoolArchiveAndRecreate(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	stream := pool.Chat(ctx, "sess", "msg")
	for range stream {
	}

	if err := pool.ArchiveSession("sess"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	// The old runner should be closed.
	if !(*runners)[0].closed {
		t.Error("old runner should be closed after ArchiveSession")
	}

	// Session should be removed.
	pool.mu.Lock()
	_, exists := pool.sessions["sess"]
	pool.mu.Unlock()
	if exists {
		t.Error("session should be removed after ArchiveSession")
	}

	// Next chat should create a new
	stream = pool.Chat(ctx, "sess", "msg2")
	for range stream {
	}

	if len(*runners) != 2 {
		t.Errorf("expected 2 runners, got %d", len(*runners))
	}
}

func TestPoolArchiveNonexistent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	// Should not error on nonexistent session.
	if err := pool.ArchiveSession("nonexistent"); err != nil {
		t.Fatalf("ArchiveSession nonexistent: %v", err)
	}
}

func TestPoolClose(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	ctx := testSessionContext()

	stream := pool.Chat(ctx, "a", "msg")
	for range stream {
	}
	stream = pool.Chat(ctx, "b", "msg")
	for range stream {
	}

	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i, r := range *runners {
		if !r.closed {
			t.Errorf("runner %d should be closed after pool.Close()", i)
		}
	}

	pool.mu.Lock()
	sessCount := len(pool.sessions)
	pool.mu.Unlock()
	if sessCount != 0 {
		t.Errorf("sessions count = %d, want 0", sessCount)
	}
}

func TestPoolReapIdle(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithIdleTimeout(1*time.Millisecond))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	// Create a session by triggering getOrCreateRunner.
	_, r, err := pool.getOrCreateRunner(ctx, "idle-sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner: %v", err)
	}

	// Wait for the runner to become idle.
	time.Sleep(50 * time.Millisecond)

	// Manually trigger reap.
	pool.reap()

	time.Sleep(100 * time.Millisecond)

	// Runner should be closed (nil'd out), but session still exists.
	pool.mu.Lock()
	sess, exists := pool.sessions["idle-sess"]
	var runnerNil bool
	if exists {
		runnerNil = sess.Runner == nil
	}
	pool.mu.Unlock()

	if !exists {
		t.Error("session should still exist after reap (history preserved)")
	}
	if !runnerNil {
		t.Error("runner should be nil after reap")
	}

	mr := r.(*mockRunner)
	if mr.Alive() {
		t.Error("idle runner should not be alive after reap")
	}
}

func TestPoolReapDead(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithIdleTimeout(10*time.Minute))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	// Create a session with a mockRunner.
	_, _, err := pool.getOrCreateRunner(ctx, "dead-sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner: %v", err)
	}

	// Kill the runner by marking it dead.
	(*runners)[0].mu.Lock()
	(*runners)[0].alive = false
	(*runners)[0].mu.Unlock()

	pool.reap()

	pool.mu.Lock()
	sess, exists := pool.sessions["dead-sess"]
	var runnerNil bool
	if exists {
		runnerNil = sess.Runner == nil
	}
	pool.mu.Unlock()

	if !exists {
		t.Error("session should still exist after reap of dead runner")
	}
	if !runnerNil {
		t.Error("dead runner should be nil'd after reap")
	}
	if !(*runners)[0].closed {
		t.Error("dead runner should be closed during reap")
	}
}

func TestPoolStartReaperCancels(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pool.StartReaper(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// OK, StartReaper returned.
	case <-time.After(2 * time.Second):
		t.Fatal("StartReaper did not return after context cancel")
	}
}

func TestPoolReplacesDeadRunnerOnChat(t *testing.T) {
	// Use mockRunner to test dead-runner replacement in getOrCreateRunner.
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	// Create a session with a
	_, _, err := pool.getOrCreateRunner(ctx, "sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner: %v", err)
	}

	// Kill the runner by marking it dead.
	(*runners)[0].mu.Lock()
	(*runners)[0].alive = false
	(*runners)[0].mu.Unlock()

	// Next call should create a new
	_, runner2, err := pool.getOrCreateRunner(ctx, "sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner after death: %v", err)
	}

	if runner2 == (*runners)[0] {
		t.Error("dead runner should be replaced with a new one")
	}
	if len(*runners) != 2 {
		t.Errorf("expected 2 runners created, got %d", len(*runners))
	}
}

func TestPoolCreateSession(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, err := pool.CreateSession("test", "test-user")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.ID == "" {
		t.Error("session ID should not be empty")
	}
	if info.Archived {
		t.Error("new session should not be archived")
	}
	if info.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestPoolCreateAndListSessions(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	_, _ = pool.CreateSession("test", "test-user")
	_, _ = pool.CreateSession("test", "test-user")

	sessions, err := pool.ListSessions(false, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestPoolActiveSession(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// No sessions yet.
	_, ok := pool.ActiveSession("cli")
	if ok {
		t.Error("expected no active session")
	}

	// Create a session in "cli" channel.
	info, _ := pool.CreateSession("cli")

	got, ok := pool.ActiveSession("cli")
	if !ok {
		t.Fatal("expected active session")
	}
	if got.ID != info.ID {
		t.Errorf("got ID %q, want %q", got.ID, info.ID)
	}
	if got.Channel != "cli" {
		t.Errorf("got Channel %q, want %q", got.Channel, "cli")
	}

	// Different channel should not find it.
	_, ok = pool.ActiveSession("tg123")
	if ok {
		t.Error("expected no active session for tg123")
	}
}

func TestPoolResolveSession(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	// First call creates a new session.
	info1, err := pool.ResolveSession(ctx, "cli", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if info1.Channel != "cli" {
		t.Errorf("Channel = %q, want cli", info1.Channel)
	}

	// Second call returns the same session.
	info2, err := pool.ResolveSession(ctx, "cli", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if info2.ID != info1.ID {
		t.Errorf("second resolve returned different ID: %q vs %q", info2.ID, info1.ID)
	}

	// Archive and resolve again — should create a new session.
	_ = pool.ArchiveSession(info1.ID)
	info3, err := pool.ResolveSession(ctx, "cli", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if info3.ID == info1.ID {
		t.Error("expected new session after archive")
	}
}

func TestPoolResolveSessionTwoUsersSameAgent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("agentA"))
	defer func() { _ = pool.Close() }()

	ctxA := memory.WithAgentID(memory.WithUserID(context.Background(), "userA"), "agentA")
	ctxB := memory.WithAgentID(memory.WithUserID(context.Background(), "userB"), "agentA")

	sessionA, err := pool.ResolveSession(ctxA, "agentA:user:userA:private", "userA")
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := pool.ResolveSession(ctxB, "agentA:user:userB:private", "userB")
	if err != nil {
		t.Fatal(err)
	}
	if sessionA.ID == sessionB.ID {
		t.Fatalf("expected different sessions for different users, got %q", sessionA.ID)
	}
	if sessionA.UserID != "userA" {
		t.Errorf("sessionA user = %q, want userA", sessionA.UserID)
	}
	if sessionB.UserID != "userB" {
		t.Errorf("sessionB user = %q, want userB", sessionB.UserID)
	}

	againA, err := pool.ResolveSession(ctxA, "agentA:user:userA:private", "userA")
	if err != nil {
		t.Fatal(err)
	}
	againB, err := pool.ResolveSession(ctxB, "agentA:user:userB:private", "userB")
	if err != nil {
		t.Fatal(err)
	}
	if againA.ID != sessionA.ID {
		t.Errorf("userA resolve = %q, want %q", againA.ID, sessionA.ID)
	}
	if againB.ID != sessionB.ID {
		t.Errorf("userB resolve = %q, want %q", againB.ID, sessionB.ID)
	}
}

func TestPoolResolveSessionGroupDoesNotUsePrivateMain(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("agentA"))
	defer func() { _ = pool.Close() }()

	ctx := memory.WithAgentID(memory.WithUserID(context.Background(), "userA"), "agentA")
	privateSession, err := pool.ResolveSession(ctx, "agentA:user:userA:private", "userA")
	if err != nil {
		t.Fatal(err)
	}
	groupSession, err := pool.ResolveSession(ctx, "agentA:telegram:externalA:group:chat1", "userA")
	if err != nil {
		t.Fatal(err)
	}
	if groupSession.ID == privateSession.ID {
		t.Fatal("group session reused private main session")
	}
	if groupSession.Kind != "chat" {
		t.Errorf("group session kind = %q, want chat", groupSession.Kind)
	}

	againGroup, err := pool.ResolveSession(ctx, "agentA:telegram:externalA:group:chat1", "userA")
	if err != nil {
		t.Fatal(err)
	}
	if againGroup.ID != groupSession.ID {
		t.Errorf("group resolve = %q, want %q", againGroup.ID, groupSession.ID)
	}
}

func TestPoolResolveSessionNonUserChannelDoesNotUsePrivateMain(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("agentA"))
	defer func() { _ = pool.Close() }()

	ctx := memory.WithAgentID(memory.WithUserID(context.Background(), "userA"), "agentA")
	privateSession, err := pool.ResolveSession(ctx, "agentA:user:userA:private", "userA")
	if err != nil {
		t.Fatal(err)
	}
	jobSession, err := pool.ResolveSession(ctx, "agentA:scheduler:job1", "userA")
	if err != nil {
		t.Fatal(err)
	}
	if jobSession.ID == privateSession.ID {
		t.Fatal("non-user channel reused private main session")
	}
	if jobSession.Kind != "chat" {
		t.Errorf("job session kind = %q, want chat", jobSession.Kind)
	}

	againJob, err := pool.ResolveSession(ctx, "agentA:scheduler:job1", "userA")
	if err != nil {
		t.Fatal(err)
	}
	if againJob.ID != jobSession.ID {
		t.Errorf("job resolve = %q, want %q", againJob.ID, jobSession.ID)
	}
}

func TestPoolRotateSession(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	ch := "tg12345"

	// Create initial session and chat.
	info1, _ := pool.CreateSession(ch, "test-user")
	stream := pool.Chat(testSessionContext(), info1.ID, "hello")
	for range stream {
	}

	// Rotate: archives old, creates new.
	info2, err := pool.RotateSession(ch, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	if info2.ID == info1.ID {
		t.Error("new session should have different ID")
	}

	// New session should have no history.
	history := pool.History(info2.ID)
	if len(history) != 0 {
		t.Errorf("new session should have empty history, got %d events", len(history))
	}

	// ActiveSession should return the new one.
	active, ok := pool.ActiveSession(ch, "test-user")
	if !ok {
		t.Fatal("expected active session")
	}
	if active.ID != info2.ID {
		t.Errorf("active session = %q, want %q", active.ID, info2.ID)
	}
}

func TestPoolRotateSessionNoExisting(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Rotate with no existing session should just create one.
	info, err := pool.RotateSession("fresh", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if info.Channel != "fresh" {
		t.Errorf("Channel = %q, want fresh", info.Channel)
	}
}

func TestPoolResolveSessionConcurrent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Launch multiple goroutines that all resolve the same channel concurrently.
	const n = 20
	results := make(chan string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			info, err := pool.ResolveSession(testSessionContext(), "concurrent", "test-user")
			if err != nil {
				t.Errorf("ResolveSession: %v", err)
				return
			}
			results <- info.ID
		}()
	}
	wg.Wait()
	close(results)

	// All goroutines should have resolved to the same session ID.
	ids := make(map[string]struct{})
	for id := range results {
		ids[id] = struct{}{}
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 unique session ID, got %d: %v", len(ids), ids)
	}
}

func TestPoolActiveSessionIgnoresLegacySessions(t *testing.T) {
	// Sessions created before the Channel field was added have Channel == "".
	// They should not match any channel query.
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Simulate a legacy session by directly injecting one without a Channel.
	pool.mu.Lock()
	pool.sessions["legacy-abc"] = &Session{
		Info: SessionInfo{
			ID:         "legacy-abc",
			Channel:    "",
			CreatedAt:  time.Now(),
			LastActive: time.Now(),
		},
	}
	pool.mu.Unlock()

	// ActiveSession for "cli" should not match the legacy session.
	_, ok := pool.ActiveSession("cli")
	if ok {
		t.Error("legacy session with empty Channel should not match 'cli'")
	}

	// ResolveSession should not promote legacy sessions without user/channel scope.
	info, err := pool.ResolveSession(testSessionContext(), "cli", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID == "legacy-abc" {
		t.Error("legacy session should not be promoted")
	}
	if info.Channel != "cli" {
		t.Errorf("Channel = %q, want cli", info.Channel)
	}
}

func TestPoolArchiveSession(t *testing.T) {
	factory, runners := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	// Chat to create a runner
	stream := pool.Chat(testSessionContext(), info.ID, "test")
	for range stream {
	}

	if err := pool.ArchiveSession(info.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	// Runner should be closed
	if !(*runners)[0].closed {
		t.Error("runner should be closed after archive")
	}

	// Session should be removed from memory
	pool.mu.Lock()
	_, exists := pool.sessions[info.ID]
	pool.mu.Unlock()
	if exists {
		t.Error("session should be removed from memory after archive")
	}
}

func TestPoolGetSession(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	got, err := pool.GetSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != info.ID {
		t.Errorf("got ID %q, want %q", got.ID, info.ID)
	}
}

func TestPoolGetSessionNotFound(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	_, err := pool.GetSession("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestPoolChatAutoTitles(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "response"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	stream := pool.Chat(testSessionContext(), info.ID, "How do I fix the bug in pool.go?")
	for range stream {
	}

	pool.mu.Lock()
	sess := pool.sessions[info.ID]
	title := sess.Info.Title
	pool.mu.Unlock()

	if title == "" {
		t.Error("session should have auto-generated title")
	}
	if title != "How do I fix the bug in pool.go?" {
		t.Errorf("unexpected title: %q", title)
	}
}

func TestPoolChatAutoTitleTruncates(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	longMsg := "This is a very long message that should be truncated at a word boundary to keep the title reasonable and readable"
	stream := pool.Chat(testSessionContext(), info.ID, longMsg)
	for range stream {
	}

	pool.mu.Lock()
	title := pool.sessions[info.ID].Info.Title
	pool.mu.Unlock()

	if len(title) > 65 { // 60 + "..."
		t.Errorf("title too long (%d chars): %q", len(title), title)
	}
}

func TestPoolChatWithModel(t *testing.T) {
	// Track which model was requested for each runner creation.
	var models []string
	var mu sync.Mutex
	factory := func(_ context.Context, params RunnerParams) (Runner, error) {
		mu.Lock()
		models = append(models, params.Model)
		mu.Unlock()
		return newMockRunner([]Event{{Text: "ok"}}), nil
	}

	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithDefaultModel("default-model"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()
	info, _ := pool.CreateSession("test", "test-user")

	// First chat uses default model.
	stream := pool.Chat(ctx, info.ID, "hello")
	for range stream {
	}

	mu.Lock()
	if len(models) != 1 || models[0] != "default-model" {
		t.Fatalf("first call models = %v, want [default-model]", models)
	}
	mu.Unlock()

	// Second chat with explicit model triggers runner replacement.
	stream = pool.Chat(ctx, info.ID, "hello", WithModel("custom-model"))
	for range stream {
	}

	mu.Lock()
	if len(models) != 2 || models[1] != "custom-model" {
		t.Fatalf("second call models = %v, want [..., custom-model]", models)
	}
	mu.Unlock()

	// Third chat without model reuses the session's current model (custom-model).
	stream = pool.Chat(ctx, info.ID, "hello")
	for range stream {
	}

	mu.Lock()
	// No new runner should be created -- still 2 total.
	if len(models) != 2 {
		t.Fatalf("third call created new runner, models = %v, want len 2", models)
	}
	mu.Unlock()
}

func TestPoolFastModelForCompaction(t *testing.T) {
	mem := testMemoryProvider(t)
	factory, _ := mockRunnerFactory([]Event{{Text: "summary text"}})

	pool := NewPool(factory, mem,
		WithAgentID("test-agent"),
		WithDefaultModel("strong-model"),
		WithFastModel("fast-model"),
	)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	// Chat to create a session with history.
	stream := pool.Chat(testSessionContext(), info.ID, "hello")
	for range stream {
	}

	// Compact should succeed via memory engine.
	summary, err := pool.CompactSession(context.Background(), info.ID)
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if summary == "" {
		t.Error("expected non-empty summary from compaction")
	}
}

func TestSetDefaultModelAffectsNewSessions(t *testing.T) {
	var models []string
	var mu sync.Mutex
	factory := func(_ context.Context, params RunnerParams) (Runner, error) {
		mu.Lock()
		models = append(models, params.Model)
		mu.Unlock()
		return newMockRunner([]Event{{Text: "ok"}}), nil
	}

	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithDefaultModel("initial-model"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()

	// First session uses initial default.
	info1, _ := pool.CreateSession("test", "test-user")
	stream := pool.Chat(ctx, info1.ID, "hello")
	for range stream {
	}

	mu.Lock()
	if models[0] != "initial-model" {
		t.Fatalf("first session model = %q, want initial-model", models[0])
	}
	mu.Unlock()

	// Switch default model at runtime.
	pool.SetDefaultModel("switched-model")

	// New session should use the switched model.
	info2, _ := pool.CreateSession("test", "test-user")
	stream = pool.Chat(ctx, info2.ID, "hello")
	for range stream {
	}

	mu.Lock()
	if models[1] != "switched-model" {
		t.Fatalf("second session model = %q, want switched-model", models[1])
	}
	mu.Unlock()
}

func TestResetRunnersUsesUpdatedDefaultModelForExistingSession(t *testing.T) {
	var models []string
	var mu sync.Mutex
	factory := func(_ context.Context, params RunnerParams) (Runner, error) {
		mu.Lock()
		models = append(models, params.Model)
		mu.Unlock()
		return newMockRunner([]Event{{Text: "ok"}}), nil
	}

	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithDefaultModel("initial-model"))
	defer func() { _ = pool.Close() }()

	ctx := testSessionContext()
	info, _ := pool.CreateSession("test", "test-user")

	stream := pool.Chat(ctx, info.ID, "hello")
	for range stream {
	}

	pool.SetDefaultModel("updated-model")
	if err := pool.ResetRunners(); err != nil {
		t.Fatalf("ResetRunners: %v", err)
	}

	stream = pool.Chat(ctx, info.ID, "hello again")
	for range stream {
	}

	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 {
		t.Fatalf("created runner models = %v, want 2 runners", models)
	}
	if models[0] != "initial-model" {
		t.Fatalf("first runner model = %q, want initial-model", models[0])
	}
	if models[1] != "updated-model" {
		t.Fatalf("second runner model = %q, want updated-model", models[1])
	}
}

func TestNeedsCompactionDisabled(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithCompaction(CompactionConfig{MaxTokens: 0}))

	if pool.NeedsCompaction(context.Background(), "any-session") {
		t.Error("NeedsCompaction should return false when MaxTokens <= 0")
	}
}

func TestNeedsCompactionUnderThreshold(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "short"}})
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithCompaction(CompactionConfig{MaxTokens: 100_000}))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")
	stream := pool.Chat(testSessionContext(), info.ID, "hi")
	for range stream {
	}

	// Small conversation should not need compaction.
	if pool.NeedsCompaction(context.Background(), info.ID) {
		t.Error("small session should not need compaction")
	}
}

func TestNeedsCompactionAfterRestart(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "short"}})
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithCompaction(CompactionConfig{MaxTokens: 100_000}))
	defer func() { _ = pool.Close() }()

	ctx := memory.WithUserID(context.Background(), "user-1")
	info, _ := pool.CreateSession("test", "user-1")
	stream := pool.Chat(ctx, info.ID, "hi")
	for range stream {
	}

	// Simulate server restart: clear the in-memory session map.
	pool.mu.Lock()
	pool.sessions = make(map[string]*Session)
	pool.mu.Unlock()

	// NeedsCompaction must still find the conversation via context user_id.
	if pool.NeedsCompaction(ctx, info.ID) {
		t.Error("small session should not need compaction even after restart")
	}
}

func TestPoolCloseIdempotent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	if err := pool.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestPoolHistoryEmpty(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))

	// Nonexistent session returns empty history.
	history := pool.History("nonexistent")
	if len(history) != 0 {
		t.Errorf("expected empty history, got %d events", len(history))
	}
}

func TestPoolConcurrentChat(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, testMemoryProvider(t), WithAgentID("test-agent"), WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			sessionID := fmt.Sprintf("session-%d", i)
			stream := pool.Chat(testSessionContext(), sessionID, "hello")
			for range stream {
			}
		}()
	}
	wg.Wait()
}

func TestPoolPersistNewSessionWithMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, err := pool.CreateSession("cli", "test-user")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Session metadata should be persisted in memory provider.
	sm := mem.(memory.SessionManager)
	loaded, err := sm.LoadInfo(testSessionContext(), info.ID)
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if loaded.ID != info.ID {
		t.Errorf("loaded ID = %q, want %q", loaded.ID, info.ID)
	}
	if loaded.Channel != "cli" {
		t.Errorf("loaded Channel = %q, want %q", loaded.Channel, "cli")
	}
}

func TestPoolActiveSessionFromMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Save a session directly to the memory provider (not in pool's in-memory map).
	sm := mem.(memory.SessionManager)
	extInfo := SessionInfo{
		ID:         "ext-sess-001",
		Channel:    "telegram",
		AgentID:    "test-agent",
		UserID:     "test-user",
		LastActive: time.Now(),
	}
	if err := sm.SaveInfo(testSessionContext(), extInfo); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	// activeSessionLocked should find the session from the memory engine.
	got, ok := pool.ActiveSession("telegram", "test-user")
	if !ok {
		t.Fatal("expected active session from memory engine")
	}
	if got.ID != "ext-sess-001" {
		t.Errorf("got ID %q, want %q", got.ID, "ext-sess-001")
	}
}

func TestPoolArchiveSessionWithMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	// Chat to create a
	stream := pool.Chat(testSessionContext(), info.ID, "hello")
	for range stream {
	}

	if err := pool.ArchiveSession(info.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	// The session should be marked as archived in the memory provider.
	sm := mem.(memory.SessionManager)
	loaded, err := sm.LoadInfo(testSessionContext(), info.ID)
	if err != nil {
		t.Fatalf("LoadInfo after archive: %v", err)
	}
	if !loaded.Archived {
		t.Error("expected session to be archived in memory engine")
	}
}

func TestPoolHistoryFromMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "response"}})
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	stream := pool.Chat(testSessionContext(), info.ID, "hello")
	for range stream {
	}

	// History should return messages from memory engine.
	history := pool.History(info.ID)
	if len(history) == 0 {
		t.Fatal("expected non-empty history from memory engine")
	}

	// First message should be a UserMessage.
	if _, ok := history[0].(ai.UserMessage); !ok {
		t.Errorf("history[0] type = %T, want ai.UserMessage", history[0])
	}
}

func TestPoolChatStoreEvent(t *testing.T) {
	// Create a runner that emits a Store event (e.g., assistant message with tool call).
	toolCallMsg := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call_1", Name: "bash", Arguments: map[string]any{"cmd": "ls"}},
		},
	}
	events := []Event{
		{Text: "thinking... "},
		{Store: toolCallMsg},
		{Text: "done"},
	}
	factory, _ := mockRunnerFactory(events)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")
	stream := pool.Chat(testSessionContext(), info.ID, "run ls")

	var collected string
	for evt := range stream {
		if evt.Err != nil {
			t.Fatalf("unexpected error: %v", evt.Err)
		}
		collected += evt.Text
	}

	if collected != "thinking... done" {
		t.Errorf("collected = %q, want %q", collected, "thinking... done")
	}

	// Verify the messages were ingested into memory engine.
	history := pool.History(info.ID)
	if len(history) == 0 {
		t.Fatal("expected non-empty history after Store event")
	}
}

func TestPoolChatErrorWithBufferedText(t *testing.T) {
	// Runner emits text, then an error.
	events := []Event{
		{Text: "partial "},
		{Text: "response "},
		{Err: fmt.Errorf("stream interrupted")},
	}
	factory, _ := mockRunnerFactory(events)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")
	stream := pool.Chat(testSessionContext(), info.ID, "hello")

	var collected string
	var gotErr error
	for evt := range stream {
		if evt.Err != nil {
			gotErr = evt.Err
			break
		}
		collected += evt.Text
	}

	if gotErr == nil {
		t.Fatal("expected error from stream")
	}
	if collected != "partial response " {
		t.Errorf("collected = %q, want %q", collected, "partial response ")
	}

	// The partial text should still be persisted in the memory engine.
	history := pool.History(info.ID)
	if len(history) < 2 {
		t.Fatalf("expected at least 2 messages (user + partial), got %d", len(history))
	}
}

func TestPoolGetSessionFromMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Save session info directly in the memory provider.
	sm := mem.(memory.SessionManager)
	info := SessionInfo{
		ID:      "ext-get-sess",
		Channel: "cli",
		Title:   "External Session",
		AgentID: "test-agent",
		UserID:  "test-user",
	}
	if err := sm.SaveInfo(testSessionContext(), info); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	// GetSession should fall back to memory provider.
	got, err := pool.GetSession("ext-get-sess", "test-user")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != "ext-get-sess" {
		t.Errorf("got ID %q, want %q", got.ID, "ext-get-sess")
	}
	if got.Title != "External Session" {
		t.Errorf("got Title %q, want %q", got.Title, "External Session")
	}
}

func TestPoolListSessionsWithMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Create sessions via the pool (persisted in memory engine).
	_, _ = pool.CreateSession("cli", "test-user")
	_, _ = pool.CreateSession("telegram", "test-user")

	sessions, _ := pool.ListSessions(false, "test-user")
	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(sessions))
	}

	// Archive the first one.
	_ = pool.ArchiveSession(sessions[0].ID)

	// List without archived.
	active, err := pool.ListSessions(false, "test-user")
	if err != nil {
		t.Fatalf("ListSessions active: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active session, got %d", len(active))
	}

	// List with archived.
	all, err := pool.ListSessions(true, "test-user")
	if err != nil {
		t.Fatalf("ListSessions all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 total sessions, got %d", len(all))
	}
}

func TestPoolGetOrCreateRunnerRestoresFromMemEngine(t *testing.T) {
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	// Save session metadata in the memory provider.
	sm := mem.(memory.SessionManager)
	info := SessionInfo{
		ID:      "restore-sess",
		Channel: "cli",
		Title:   "Restored Session",
		AgentID: "test-agent",
		UserID:  "test-user",
	}
	if err := sm.SaveInfo(testSessionContext(), info); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	// getOrCreateRunner for an unknown session should restore metadata from mem.
	sess, _, err := pool.getOrCreateRunner(testSessionContext(), "restore-sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner: %v", err)
	}

	if sess.Info.ID != "restore-sess" {
		t.Errorf("session ID = %q, want %q", sess.Info.ID, "restore-sess")
	}
	if sess.Info.Title != "Restored Session" {
		t.Errorf("session Title = %q, want %q", sess.Info.Title, "Restored Session")
	}
}

func TestPoolGetOrCreateRunnerSeedsScopeFromContext(t *testing.T) {
	factory := func(_ context.Context, params RunnerParams) (Runner, error) {
		if params.UserID != "42" {
			return nil, fmt.Errorf("UserID = %q, want 42", params.UserID)
		}
		if params.AgentID != "agent-blue" {
			return nil, fmt.Errorf("AgentID = %q, want %q", params.AgentID, "agent-blue")
		}
		return newMockRunner([]Event{{Text: "ok"}}), nil
	}
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("agent-blue"))
	defer func() { _ = pool.Close() }()

	ctx := memory.WithAgentID(memory.WithUserID(context.Background(), "42"), "agent-blue")
	sess, _, err := pool.getOrCreateRunner(ctx, "scoped-sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner: %v", err)
	}
	if sess.Info.UserID != "42" {
		t.Fatalf("session UserID = %q, want 42", sess.Info.UserID)
	}
	if sess.Info.AgentID != "agent-blue" {
		t.Fatalf("session AgentID = %q, want %q", sess.Info.AgentID, "agent-blue")
	}
}

func TestPoolChatToolUseEventPassthrough(t *testing.T) {
	// ToolUse events should pass through without being stored.
	toolUseEvt := &ToolUseEvent{
		Tool:   "bash",
		Status: "running",
		Input:  "ls",
	}
	events := []Event{
		{ToolUse: toolUseEvt},
		{Text: "result"},
	}
	factory, _ := mockRunnerFactory(events)
	mem := testMemoryProvider(t)
	pool := NewPool(factory, mem, WithAgentID("test-agent"))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")
	stream := pool.Chat(testSessionContext(), info.ID, "run ls")

	var gotToolUse bool
	var collected string
	for evt := range stream {
		if evt.ToolUse != nil {
			gotToolUse = true
		}
		collected += evt.Text
	}

	if !gotToolUse {
		t.Error("expected to receive ToolUse event")
	}
	if collected != "result" {
		t.Errorf("collected = %q, want %q", collected, "result")
	}
}

func TestPoolCompactSessionWithMemEngine(t *testing.T) {
	mem := testMemoryProvider(t)
	factory, _ := mockRunnerFactory([]Event{{Text: "ok"}})
	pool := NewPool(factory, mem,
		WithAgentID("test-agent"),
		WithCompaction(CompactionConfig{MaxTokens: 100_000, KeepTail: 5}),
	)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test", "test-user")

	// Ingest enough messages to have something to compact.
	ctx := testSessionContext()
	for i := range 20 {
		msg := fmt.Sprintf("message number %d with enough content to fill tokens", i)
		stream := pool.Chat(ctx, info.ID, msg)
		for range stream {
		}
	}

	// CompactSession should succeed via memory engine.
	summary, err := pool.CompactSession(ctx, info.ID)
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if summary == "" {
		t.Error("expected non-empty summary from compaction")
	}
}
