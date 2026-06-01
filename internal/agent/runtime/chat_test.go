package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type recordingMemory struct {
	messages []ai.Message
}

func (m *recordingMemory) Name() string { return "recording" }

func (m *recordingMemory) Bootstrap(context.Context, memory.Session) error { return nil }

func (m *recordingMemory) Append(_ context.Context, _ memory.Session, msgs ...ai.Message) error {
	m.messages = append(m.messages, msgs...)
	return nil
}

func (m *recordingMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, nil
}

func (m *recordingMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}

func (m *recordingMemory) Close() error { return nil }

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

	rt.streamEvents(context.Background(), "session-1", memory.Session{ID: "session-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now())
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

	rt.streamEvents(context.Background(), "sess-1", memory.Session{ID: "sess-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now())

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

	rt.streamEvents(context.Background(), "sess-1", memory.Session{ID: "sess-1"}, stream, out, hooks.NewHookSet(nil), hooks.HookMeta{}, time.Now())

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
