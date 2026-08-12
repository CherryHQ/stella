package tracehook

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// newRecordingHook installs a process-global tracer provider backed by an
// in-memory recorder, returning an enabled hook plus the recorder. The prior
// global provider is restored on cleanup so tests don't leak a recording
// provider into one another.
func newRecordingHook(t *testing.T, recordIO bool) (*Hook, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	h := New(true, recordIO)
	t.Cleanup(func() {
		_ = h.Close()
		otel.SetTracerProvider(prev)
	})
	return h, sr
}

func endedStubs(sr *tracetest.SpanRecorder) tracetest.SpanStubs {
	return tracetest.SpanStubsFromReadOnlySpans(sr.Ended())
}

// driveSession runs a full Pre/Post lifecycle for one chat session so tests can
// assert on the resulting span tree.
func driveSession(h *Hook, sessionID string, llmErr error) {
	meta := hooks.HookMeta{SessionID: sessionID, AgentID: "agent-1", UserID: "u1"}

	h.OnPreAgentCall(context.Background(), &hooks.PreAgentCallContext{
		HookMeta: meta, MessageLen: 12, Channel: "cli",
	})
	_, _ = h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{
		HookMeta: meta, Model: "claude-3", API: "anthropic", MessageCount: 2,
	})
	_, _ = h.OnPreToolCall(context.Background(), &hooks.PreToolCallContext{
		HookMeta: meta, ToolName: "bash", ToolCallID: "call-1",
		Arguments: map[string]any{"command": "echo hi"},
	})
	h.OnPostToolCall(context.Background(), &hooks.PostToolCallContext{
		HookMeta: meta, ToolName: "bash", ToolCallID: "call-1",
		Result: "hi", Duration: time.Millisecond,
	})
	// PostMemory needs the span carried in the context returned by Pre; emulate
	// the engine threading that context through.
	memRes, _ := h.OnPreMemoryCall(context.Background(), &hooks.PreMemoryCallContext{
		HookMeta: meta, Op: hooks.MemoryOpSearch, SessionID: sessionID,
	})
	h.OnPostMemoryCall(memRes.Context, &hooks.PostMemoryCallContext{
		HookMeta: meta, Op: hooks.MemoryOpSearch, SessionID: sessionID,
		Duration: time.Millisecond, ResultCount: 3,
	})
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: meta, Model: "claude-3", API: "anthropic",
		Usage: ai.Usage{InputTokens: 10, OutputTokens: 5, CacheRead: 7, CacheWrite: 3}, Duration: time.Second,
		Error: llmErr,
	})
	h.OnPostAgentCall(context.Background(), &hooks.PostAgentCallContext{
		HookMeta: meta, Duration: 2 * time.Second,
	})
}

func spanByName(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, s := range spans {
		if s.Name == name {
			return s, true
		}
	}
	return tracetest.SpanStub{}, false
}

func attrValue(s tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func assertChildOf(t *testing.T, name string, child, parent tracetest.SpanStub) {
	t.Helper()
	if child.Parent.SpanID() != parent.SpanContext.SpanID() {
		t.Errorf("%s parent = %v, want %v", name, child.Parent.SpanID(), parent.SpanContext.SpanID())
	}
}

func TestHook_EnabledSpanHierarchy(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	driveSession(h, "sess-1", nil)

	spans := endedStubs(sr)
	names := map[string]int{}
	for _, s := range spans {
		names[s.Name]++
	}

	// Each span must be ended exactly once: a leaked (never-ended) span is absent
	// from Ended() entirely, and a double-End would inflate the count.
	for _, want := range []string{"agent.loop", "turn 1", "gen_ai.chat", "gen_ai.execute_tool", "memory.search"} {
		if names[want] != 1 {
			t.Errorf("span %q ended %d times, want 1", want, names[want])
		}
	}

	loop, _ := spanByName(spans, "agent.loop")
	turn, _ := spanByName(spans, "turn 1")
	chat, _ := spanByName(spans, "gen_ai.chat")
	tool, _ := spanByName(spans, "gen_ai.execute_tool")
	mem, _ := spanByName(spans, "memory.search")

	if loop.Parent.IsValid() {
		t.Errorf("agent.loop should be a root span, got parent %v", loop.Parent.SpanID())
	}
	assertChildOf(t, "turn 1", turn, loop)
	assertChildOf(t, "gen_ai.chat", chat, turn)
	assertChildOf(t, "gen_ai.execute_tool", tool, turn)
	assertChildOf(t, "memory.search", mem, turn)
	if got, ok := attrValue(chat, "gen_ai.usage.cache_read.input_tokens"); !ok || got.AsInt64() != 7 {
		t.Errorf("cache read tokens = %d (ok=%v), want 7", got.AsInt64(), ok)
	}
	if got, ok := attrValue(chat, "gen_ai.usage.cache_creation.input_tokens"); !ok || got.AsInt64() != 3 {
		t.Errorf("cache creation tokens = %d (ok=%v), want 3", got.AsInt64(), ok)
	}
}

func TestHook_ToolIORecording(t *testing.T) {
	t.Run("default omits tool input/result", func(t *testing.T) {
		h, sr := newRecordingHook(t, false)
		driveSession(h, "sess-off", nil)

		tool, ok := spanByName(endedStubs(sr), "gen_ai.execute_tool")
		if !ok {
			t.Fatal("missing gen_ai.execute_tool span")
		}
		if _, ok := attrValue(tool, "gen_ai.tool.input"); ok {
			t.Error("gen_ai.tool.input recorded without opt-in")
		}
		if _, ok := attrValue(tool, "gen_ai.tool.result"); ok {
			t.Error("gen_ai.tool.result recorded without opt-in")
		}
		if v, ok := attrValue(tool, "gen_ai.tool.argument_count"); !ok || v.AsInt64() != 1 {
			t.Errorf("argument_count = %d (ok=%v), want 1", v.AsInt64(), ok)
		}
	})

	t.Run("opt-in records tool input/result", func(t *testing.T) {
		h, sr := newRecordingHook(t, true)
		driveSession(h, "sess-on", nil)

		tool, ok := spanByName(endedStubs(sr), "gen_ai.execute_tool")
		if !ok {
			t.Fatal("missing gen_ai.execute_tool span")
		}
		in, ok := attrValue(tool, "gen_ai.tool.input")
		if !ok {
			t.Fatal("gen_ai.tool.input not recorded under opt-in")
		}
		if in.AsString() != "echo hi" {
			t.Errorf("tool.input = %q, want %q", in.AsString(), "echo hi")
		}
		if _, ok := attrValue(tool, "gen_ai.tool.result"); !ok {
			t.Error("gen_ai.tool.result not recorded under opt-in")
		}
	})
}

func TestHook_DuplicatePostLLMCall(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	meta := hooks.HookMeta{SessionID: "dup", AgentID: "agent-1", UserID: "u1"}

	h.OnPreAgentCall(context.Background(), &hooks.PreAgentCallContext{HookMeta: meta})
	_, _ = h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{HookMeta: meta, Model: "m"})
	post := &hooks.PostLLMCallContext{HookMeta: meta, Model: "m", Duration: time.Second}
	// First Post claims and ends the span; the second must be a no-op (the span
	// was nil'd under the lock) rather than ending it again or underflowing.
	h.OnPostLLMCall(context.Background(), post)
	h.OnPostLLMCall(context.Background(), post)
	h.OnPostAgentCall(context.Background(), &hooks.PostAgentCallContext{HookMeta: meta})

	count := 0
	for _, s := range endedStubs(sr) {
		if s.Name == "gen_ai.chat" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("gen_ai.chat ended %d times, want 1", count)
	}
}

func TestHook_SpanErrorRedacted(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	secret := "sk-abcdef0123456789xyz"
	driveSession(h, "sess-err", errors.New("provider failed: api_key="+secret))

	chat, ok := spanByName(endedStubs(sr), "gen_ai.chat")
	if !ok {
		t.Fatal("missing gen_ai.chat span")
	}
	if chat.Status.Code != codes.Error {
		t.Errorf("status code = %v, want Error", chat.Status.Code)
	}
	if strings.Contains(chat.Status.Description, secret) {
		t.Errorf("status description leaks secret: %q", chat.Status.Description)
	}
	if !strings.Contains(chat.Status.Description, "[REDACTED]") {
		t.Errorf("status description missing [REDACTED]: %q", chat.Status.Description)
	}
	// The recorded exception event must also be redacted.
	for _, ev := range chat.Events {
		for _, kv := range ev.Attributes {
			if string(kv.Key) == "exception.message" && strings.Contains(kv.Value.AsString(), secret) {
				t.Errorf("exception event leaks secret: %q", kv.Value.AsString())
			}
		}
	}
}
