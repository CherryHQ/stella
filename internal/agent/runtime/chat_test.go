package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type recordingMemory struct {
	mu            sync.Mutex
	messages      []ai.Message
	commits       []int64
	appendError   error
	assembleError error
}

func (m *recordingMemory) Name() string { return "recording" }

func (m *recordingMemory) Bootstrap(context.Context, memory.Session) error { return nil }

func (m *recordingMemory) Append(_ context.Context, _ memory.Session, msgs ...ai.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendError != nil {
		return m.appendError
	}
	m.messages = append(m.messages, msgs...)
	return nil
}

func (m *recordingMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, m.assembleError
}

func (m *recordingMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}

func (m *recordingMemory) Close() error { return nil }

func (m *recordingMemory) CommitGroupCursor(_ context.Context, _ memory.Session, seq int64) error {
	m.mu.Lock()
	m.commits = append(m.commits, seq)
	m.mu.Unlock()
	return nil
}

type chatFakeRunner struct {
	events   []Event
	system   string
	messages *[]MessageContent
}

func (r chatFakeRunner) Chat(_ context.Context, _ []ai.Message, msg MessageContent) <-chan Event {
	if r.messages != nil {
		*r.messages = append(*r.messages, msg)
	}
	ch := make(chan Event, len(r.events))
	for _, evt := range r.events {
		ch <- evt
	}
	close(ch)
	return ch
}

func (r chatFakeRunner) Alive() bool             { return true }
func (r chatFakeRunner) Busy() bool              { return false }
func (r chatFakeRunner) LastActivity() time.Time { return time.Now() }
func (r chatFakeRunner) SystemPrompt() string    { return r.system }
func (r chatFakeRunner) Close() error            { return nil }

func TestRuntimeChatCommitsGroupCursorAfterSuccessfulGroupTurn(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 1 || mem.commits[0] != 42 {
		t.Fatalf("commits = %v, want [42]", mem.commits)
	}
	if len(mem.messages) != 2 {
		t.Fatalf("messages = %d, want user + assistant", len(mem.messages))
	}
	if got := flattenRuntimeUserMessage(mem.messages[0]); got != "hello" {
		t.Fatalf("persisted user = %q", got)
	}
	if _, ok := mem.messages[1].(ai.AssistantMessage); !ok {
		t.Fatalf("second persisted message = %T, want assistant", mem.messages[1])
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorOnChatError(t *testing.T) {
	mem := &recordingMemory{}
	boom := errors.New("boom")
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Err: boom}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none on failed group turn", len(mem.messages))
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorWhenContextCanceled(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx, cancel := context.WithCancel(memory.WithGroupSeq(context.Background(), 42))
	cancel()
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
}

func TestRuntimeChatDoesNotPersistGroupPartialOnTimeout(t *testing.T) {
	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "partial"}, {Err: ErrChatTimeout}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none on timeout", len(mem.messages))
	}
}

func TestRuntimeChatDoesNotPersistGroupStoreBeforeLaterError(t *testing.T) {
	mem := &recordingMemory{}
	boom := errors.New("boom")
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Store: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "stored"}}}}, {Err: boom}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none after store then error", len(mem.messages))
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorWhenStoreFails(t *testing.T) {
	mem := &recordingMemory{appendError: errors.New("append failed")}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	for range out {
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
}

func TestRuntimeChatDoesNotCommitGroupCursorWhenAssembleFails(t *testing.T) {
	assembleErr := errors.New("assemble failed")
	mem := &recordingMemory{assembleError: assembleErr}
	rt, err := New(Config{
		Memory: mem,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return chatFakeRunner{events: []Event{{Text: "ok"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx := memory.WithGroupSeq(context.Background(), 42)
	out := make(chan Event, 10)
	rt.chat(ctx, out, session.Info{ID: "sess-1", UserID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", GroupID: "11111111-1111-4111-8111-111111111111"}, "hello", chatOptions{})
	var gotErr bool
	for evt := range out {
		if errors.Is(evt.Err, assembleErr) {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("expected assemble error event")
	}
	if len(mem.commits) != 0 {
		t.Fatalf("commits = %v, want none", mem.commits)
	}
	if len(mem.messages) != 0 {
		t.Fatalf("messages = %d, want none on assemble failure", len(mem.messages))
	}
}

func flattenRuntimeUserMessage(msg ai.Message) string {
	um, ok := msg.(ai.UserMessage)
	if !ok {
		return ""
	}
	switch c := um.Content.(type) {
	case string:
		return c
	case []ai.ContentBlock:
		return ai.FlattenText(c)
	default:
		return fmt.Sprintf("%v", c)
	}
}

func TestStreamEventsDoesNotDuplicateBufferedAssistantStore(t *testing.T) {
	mem := &recordingMemory{}
	rt := &Runtime{mem: mem, log: slog.Default()}

	stream := make(chan Event, 3)
	out := make(chan Event, 3)
	stream <- Event{Reasoning: "thinking"}
	stream <- Event{Text: "answer"}
	stream <- Event{Store: ai.AssistantMessage{Content: []ai.ContentBlock{
		ai.ThinkingContent{Thinking: "thinking"},
		ai.TextContent{Text: "answer"},
		ai.ToolCall{ID: "tool-1", Name: "search", Arguments: map[string]any{"q": "x"}},
	}}}
	close(stream)

	if err := rt.streamEvents(context.Background(), "session-1", memory.Session{ID: "session-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now()); err != nil {
		t.Fatalf("stream events: %v", err)
	}
	for range out {
	}

	if len(mem.messages) != 1 {
		t.Fatalf("expected one persisted assistant message, got %d", len(mem.messages))
	}
	msg, ok := mem.messages[0].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("expected assistant message, got %T", mem.messages[0])
	}
	if len(msg.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(msg.Content))
	}
	if got := msg.Content[0].(ai.ThinkingContent).Thinking; got != "thinking" {
		t.Fatalf("thinking = %q", got)
	}
	if got := msg.Content[1].(ai.TextContent).Text; got != "answer" {
		t.Fatalf("text = %q", got)
	}
}

func TestStreamEvents_TimeoutDoesNotForwardError(t *testing.T) {
	mem := &recordingMemory{}
	rt := &Runtime{mem: mem, log: slog.Default()}

	stream := make(chan Event, 3)
	out := make(chan Event, 10)
	stream <- Event{Text: "partial"}
	stream <- Event{Err: ErrChatTimeout}
	close(stream)

	if err := rt.streamEvents(context.Background(), "sess-1", memory.Session{ID: "sess-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now()); !errors.Is(err, ErrChatTimeout) {
		t.Fatalf("stream events error = %v, want timeout", err)
	}

	var events []Event
	for evt := range out {
		events = append(events, evt)
	}

	// Should have: text "partial", then the timeout notice text.
	// Should NOT have an Err event.
	for _, evt := range events {
		if evt.Err != nil {
			t.Fatalf("timeout should not forward error to caller, got: %v", evt.Err)
		}
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (partial + notice), got %d", len(events))
	}
}

func TestStreamEvents_NonTimeoutErrorForwarded(t *testing.T) {
	mem := &recordingMemory{}
	rt := &Runtime{mem: mem, log: slog.Default()}

	stream := make(chan Event, 2)
	out := make(chan Event, 10)
	realErr := fmt.Errorf("provider error")
	stream <- Event{Err: realErr}
	close(stream)

	if err := rt.streamEvents(context.Background(), "sess-1", memory.Session{ID: "sess-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now()); !errors.Is(err, realErr) {
		t.Fatalf("stream events error = %v, want provider error", err)
	}

	var gotErr bool
	for evt := range out {
		if evt.Err != nil && errors.Is(evt.Err, realErr) {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("non-timeout errors should be forwarded to caller")
	}
}
