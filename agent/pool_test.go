package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/store"
)

// mockRunner implements runner.Runner and io.Closer for pool tests.
type mockRunner struct {
	mu           sync.Mutex
	events       []runner.Event
	closed       bool
	lastActivity time.Time
	alive        bool
}

func newMockRunner(events []runner.Event) *mockRunner {
	return &mockRunner{
		events:       events,
		lastActivity: time.Now(),
		alive:        true,
	}
}

func (m *mockRunner) Chat(_ context.Context, _ []runner.RPCEvent, _ runner.MessageContent) <-chan runner.Event {
	m.mu.Lock()
	m.lastActivity = time.Now()
	events := m.events
	m.mu.Unlock()

	out := make(chan runner.Event, len(events))
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

// mockRunnerFactory returns a NewRunnerFunc that creates mockRunners with the
// given canned events. It also tracks all created runners.
func mockRunnerFactory(events []runner.Event) (runner.NewRunnerFunc, *[]*mockRunner) {
	var runners []*mockRunner
	var mu sync.Mutex
	factory := func(_ context.Context, _ string) (runner.Runner, error) {
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
	pool := NewPool(factory, WithIdleTimeout(5*time.Minute))

	if pool.idleTimeout != 5*time.Minute {
		t.Errorf("idleTimeout = %v, want 5m", pool.idleTimeout)
	}
}

func TestWithCompaction(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	cfg := CompactionConfig{MaxTokens: 40_000, KeepTail: 10}
	pool := NewPool(factory, WithCompaction(cfg))

	if pool.compaction.MaxTokens != 40_000 {
		t.Errorf("MaxTokens = %d, want 40000", pool.compaction.MaxTokens)
	}
	if pool.compaction.KeepTail != 10 {
		t.Errorf("KeepTail = %d, want 10", pool.compaction.KeepTail)
	}
}

func TestSetFactory(t *testing.T) {
	factory1, _ := mockRunnerFactory(nil)
	pool := NewPool(factory1)

	factory2, _ := mockRunnerFactory([]runner.Event{{Text: "new"}})
	pool.SetFactory(factory2)

	// Verify factory was replaced by creating a session
	ctx := context.Background()
	info, _ := pool.CreateSession("test")
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
	events := []runner.Event{
		{Text: "Hello "},
		{Text: "world"},
	}
	factory, _ := mockRunnerFactory(events)
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ctx := context.Background()
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

func TestPoolChatReusesSession(t *testing.T) {
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// First chat creates a runner.
	stream := pool.Chat(ctx, "sess-1", "first")
	for range stream {
	}

	// Second chat should reuse the same runner.
	stream = pool.Chat(ctx, "sess-1", "second")
	for range stream {
	}

	if len(*runners) != 1 {
		t.Errorf("expected 1 runner created, got %d", len(*runners))
	}
}

func TestPoolChatMultipleSessions(t *testing.T) {
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

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
	events := []runner.Event{
		{Text: "chunk1"},
		{Text: "chunk2"},
	}
	factory, _ := mockRunnerFactory(events)
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	stream := pool.Chat(ctx, "sess", "msg")
	for range stream {
	}

	pool.mu.Lock()
	sess := pool.sessions["sess"]
	histLen := len(sess.Events)
	pool.mu.Unlock()

	// 1 user_message + 2 text deltas = 3 events.
	if histLen != 3 {
		t.Errorf("history length = %d, want 3", histLen)
	}
}

func TestPoolChatErrorFromFactory(t *testing.T) {
	factory := func(_ context.Context, _ string) (runner.Runner, error) {
		return nil, fmt.Errorf("factory error")
	}
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	stream := pool.Chat(context.Background(), "sess", "msg")

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
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

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

	// Next chat should create a new runner.
	stream = pool.Chat(ctx, "sess", "msg2")
	for range stream {
	}

	if len(*runners) != 2 {
		t.Errorf("expected 2 runners, got %d", len(*runners))
	}
}

func TestPoolArchiveNonexistent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)

	// Should not error on nonexistent session.
	if err := pool.ArchiveSession("nonexistent"); err != nil {
		t.Fatalf("ArchiveSession nonexistent: %v", err)
	}
}

func TestPoolClose(t *testing.T) {
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)

	ctx := context.Background()

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
	factory, _ := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory, WithIdleTimeout(1*time.Millisecond))
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

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
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory, WithIdleTimeout(10*time.Minute))
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

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
}

func TestPoolStartReaperCancels(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)

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
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Create a session with a runner.
	_, _, err := pool.getOrCreateRunner(ctx, "sess", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner: %v", err)
	}

	// Kill the runner by marking it dead.
	(*runners)[0].mu.Lock()
	(*runners)[0].alive = false
	(*runners)[0].mu.Unlock()

	// Next call should create a new runner.
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
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	info, err := pool.CreateSession("test")
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
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	_, _ = pool.CreateSession("test")
	_, _ = pool.CreateSession("test")

	sessions, err := pool.ListSessions(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestPoolActiveSession(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)
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
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	// First call creates a new session.
	info1, err := pool.ResolveSession("cli")
	if err != nil {
		t.Fatal(err)
	}
	if info1.Channel != "cli" {
		t.Errorf("Channel = %q, want cli", info1.Channel)
	}

	// Second call returns the same session.
	info2, err := pool.ResolveSession("cli")
	if err != nil {
		t.Fatal(err)
	}
	if info2.ID != info1.ID {
		t.Errorf("second resolve returned different ID: %q vs %q", info2.ID, info1.ID)
	}

	// Archive and resolve again — should create a new session.
	_ = pool.ArchiveSession(info1.ID)
	info3, err := pool.ResolveSession("cli")
	if err != nil {
		t.Fatal(err)
	}
	if info3.ID == info1.ID {
		t.Error("expected new session after archive")
	}
}

func TestPoolRotateSession(t *testing.T) {
	factory, _ := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	ch := "tg12345"

	// Create initial session and chat.
	info1, _ := pool.CreateSession(ch)
	stream := pool.Chat(context.Background(), info1.ID, "hello")
	for range stream {
	}

	// Rotate: archives old, creates new.
	info2, err := pool.RotateSession(ch)
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
	active, ok := pool.ActiveSession(ch)
	if !ok {
		t.Fatal("expected active session")
	}
	if active.ID != info2.ID {
		t.Errorf("active session = %q, want %q", active.ID, info2.ID)
	}
}

func TestPoolRotateSessionNoExisting(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	// Rotate with no existing session should just create one.
	info, err := pool.RotateSession("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Channel != "fresh" {
		t.Errorf("Channel = %q, want fresh", info.Channel)
	}
}

func TestPoolResolveSessionConcurrent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	// Launch multiple goroutines that all resolve the same channel concurrently.
	const n = 20
	results := make(chan string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			info, err := pool.ResolveSession("concurrent")
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
	pool := NewPool(factory)
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

	// ResolveSession should create a new one instead.
	info, err := pool.ResolveSession("cli")
	if err != nil {
		t.Fatal(err)
	}
	if info.Channel != "cli" {
		t.Errorf("Channel = %q, want cli", info.Channel)
	}
	if info.ID == "legacy-abc" {
		t.Error("should not reuse legacy session")
	}
}

func TestPoolArchiveSession(t *testing.T) {
	factory, runners := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test")

	// Chat to create a runner
	stream := pool.Chat(context.Background(), info.ID, "test")
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
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test")

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
	pool := NewPool(factory)

	_, err := pool.GetSession("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestPoolChatAutoTitles(t *testing.T) {
	factory, _ := mockRunnerFactory([]runner.Event{{Text: "response"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test")

	stream := pool.Chat(context.Background(), info.ID, "How do I fix the bug in pool.go?")
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
	factory, _ := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test")

	longMsg := "This is a very long message that should be truncated at a word boundary to keep the title reasonable and readable"
	stream := pool.Chat(context.Background(), info.ID, longMsg)
	for range stream {
	}

	pool.mu.Lock()
	title := pool.sessions[info.ID].Info.Title
	pool.mu.Unlock()

	if len(title) > 65 { // 60 + "…"
		t.Errorf("title too long (%d chars): %q", len(title), title)
	}
}

func TestPoolChatWithModel(t *testing.T) {
	// Track which model was requested for each runner creation.
	var models []string
	var mu sync.Mutex
	factory := func(_ context.Context, model string) (runner.Runner, error) {
		mu.Lock()
		models = append(models, model)
		mu.Unlock()
		return newMockRunner([]runner.Event{{Text: "ok"}}), nil
	}

	pool := NewPool(factory, WithDefaultModel("default-model"))
	defer func() { _ = pool.Close() }()

	ctx := context.Background()
	info, _ := pool.CreateSession("test")

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
	// No new runner should be created — still 2 total.
	if len(models) != 2 {
		t.Fatalf("third call created new runner, models = %v, want len 2", models)
	}
	mu.Unlock()
}

func TestPoolFastModelForCompaction(t *testing.T) {
	var models []string
	var mu sync.Mutex
	factory := func(_ context.Context, model string) (runner.Runner, error) {
		mu.Lock()
		models = append(models, model)
		mu.Unlock()
		return newMockRunner([]runner.Event{{Text: "summary text"}}), nil
	}

	dir := t.TempDir()
	s, _ := store.NewFileStore(dir, t.TempDir())
	pool := NewPool(factory,
		WithDefaultModel("strong-model"),
		WithFastModel("fast-model"),
		WithStore(s),
	)
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test")

	// Chat to create a session with history.
	stream := pool.Chat(context.Background(), info.ID, "hello")
	for range stream {
	}

	mu.Lock()
	if models[0] != "strong-model" {
		t.Fatalf("chat model = %q, want strong-model", models[0])
	}
	mu.Unlock()

	// Compact should use fast model.
	_, err := pool.CompactSession(context.Background(), info.ID)
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}

	mu.Lock()
	// The compaction should have created a runner with fast-model.
	found := false
	for _, m := range models {
		if m == "fast-model" {
			found = true
			break
		}
	}
	mu.Unlock()

	if !found {
		t.Errorf("compaction did not use fast model, models = %v", models)
	}

	// After compaction, session model should be restored to strong-model
	// so subsequent chats don't stay on the fast tier.
	pool.mu.Lock()
	sessModel := pool.sessions[info.ID].Model
	pool.mu.Unlock()

	if sessModel != "strong-model" {
		t.Errorf("session model after compaction = %q, want %q", sessModel, "strong-model")
	}
}

func TestSetDefaultModelAffectsNewSessions(t *testing.T) {
	var models []string
	var mu sync.Mutex
	factory := func(_ context.Context, model string) (runner.Runner, error) {
		mu.Lock()
		models = append(models, model)
		mu.Unlock()
		return newMockRunner([]runner.Event{{Text: "ok"}}), nil
	}

	pool := NewPool(factory, WithDefaultModel("initial-model"))
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// First session uses initial default.
	info1, _ := pool.CreateSession("test")
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
	info2, _ := pool.CreateSession("test")
	stream = pool.Chat(ctx, info2.ID, "hello")
	for range stream {
	}

	mu.Lock()
	if models[1] != "switched-model" {
		t.Fatalf("second session model = %q, want switched-model", models[1])
	}
	mu.Unlock()
}

func TestNeedsCompactionNoStore(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory, WithCompaction(CompactionConfig{MaxTokens: 100}))

	// No store set — should always return false.
	if pool.NeedsCompaction("any-session") {
		t.Error("NeedsCompaction should return false when store is nil")
	}
}

func TestNeedsCompactionDisabled(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	dir := t.TempDir()
	s, _ := store.NewFileStore(dir, t.TempDir())
	pool := NewPool(factory, WithStore(s), WithCompaction(CompactionConfig{MaxTokens: 0}))

	if pool.NeedsCompaction("any-session") {
		t.Error("NeedsCompaction should return false when MaxTokens <= 0")
	}
}

func TestNeedsCompactionUnderThreshold(t *testing.T) {
	factory, _ := mockRunnerFactory([]runner.Event{{Text: "short"}})
	dir := t.TempDir()
	s, _ := store.NewFileStore(dir, t.TempDir())
	pool := NewPool(factory, WithStore(s), WithCompaction(CompactionConfig{MaxTokens: 100_000}))
	defer func() { _ = pool.Close() }()

	info, _ := pool.CreateSession("test")
	stream := pool.Chat(context.Background(), info.ID, "hi")
	for range stream {
	}

	// Small conversation should not need compaction.
	if pool.NeedsCompaction(info.ID) {
		t.Error("small session should not need compaction")
	}
}

func TestPoolCloseIdempotent(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)

	if err := pool.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestPoolHistoryEmpty(t *testing.T) {
	factory, _ := mockRunnerFactory(nil)
	pool := NewPool(factory)

	// Nonexistent session returns empty history.
	history := pool.History("nonexistent")
	if len(history) != 0 {
		t.Errorf("expected empty history, got %d events", len(history))
	}
}

func TestPoolConcurrentChat(t *testing.T) {
	factory, _ := mockRunnerFactory([]runner.Event{{Text: "ok"}})
	pool := NewPool(factory)
	defer func() { _ = pool.Close() }()

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			sessionID := fmt.Sprintf("session-%d", i)
			stream := pool.Chat(context.Background(), sessionID, "hello")
			for range stream {
			}
		}()
	}
	wg.Wait()
}
