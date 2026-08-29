package tracehook

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/CherryHQ/stella/internal/observability"
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
	for _, want := range []string{"agent.loop", "agent.turn", "gen_ai.chat", "gen_ai.execute_tool", "memory.search"} {
		if names[want] != 1 {
			t.Errorf("span %q ended %d times, want 1", want, names[want])
		}
	}

	loop, _ := spanByName(spans, "agent.loop")
	turn, _ := spanByName(spans, "agent.turn")
	chat, _ := spanByName(spans, "gen_ai.chat")
	tool, _ := spanByName(spans, "gen_ai.execute_tool")
	mem, _ := spanByName(spans, "memory.search")

	if loop.Parent.IsValid() {
		t.Errorf("agent.loop should be a root span, got parent %v", loop.Parent.SpanID())
	}
	assertChildOf(t, "agent.turn", turn, loop)
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
		if _, ok := attrValue(tool, "stella.tool.input"); ok {
			t.Error("gen_ai.tool.input recorded without opt-in")
		}
		if _, ok := attrValue(tool, "stella.tool.result"); ok {
			t.Error("gen_ai.tool.result recorded without opt-in")
		}
		if v, ok := attrValue(tool, "stella.tool.argument_count"); !ok || v.AsInt64() != 1 {
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
		in, ok := attrValue(tool, "stella.tool.input")
		if !ok {
			t.Fatal("gen_ai.tool.input not recorded under opt-in")
		}
		if in.AsString() != "echo hi" {
			t.Errorf("tool.input = %q, want %q", in.AsString(), "echo hi")
		}
		if _, ok := attrValue(tool, "stella.tool.result"); !ok {
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

// A blacklist cannot know every credential-shaped parameter a gateway
// invents, so the error message does not leave the process at all: the span
// carries the Go type and a fixed description, nothing derived from the text.
func TestHook_SpanErrorCarriesNoProviderText(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	secret := "sk-abcdef0123456789xyz"
	raw := "provider failed: POST https://gw.example/v1?auth_blob=" + secret + " -> 401 unauthorized"
	driveSession(h, "sess-err", errors.New(raw))

	chat, ok := spanByName(endedStubs(sr), "gen_ai.chat")
	if !ok {
		t.Fatal("missing gen_ai.chat span")
	}
	if chat.Status.Code != codes.Error {
		t.Errorf("status code = %v, want Error", chat.Status.Code)
	}
	if chat.Status.Description != "model call failed" {
		t.Errorf("status description = %q, want the fixed text", chat.Status.Description)
	}
	if v, ok := attrValue(chat, "error.type"); !ok || v.AsString() != "*errors.errorString" {
		t.Errorf("error.type = %q (ok=%v), want the Go type", v.AsString(), ok)
	}
	// Nothing anywhere on the span may echo the message: not the status, not
	// an exception event, not an attribute.
	for _, ev := range chat.Events {
		if ev.Name == "exception" {
			t.Errorf("span recorded an exception event: %v", ev.Attributes)
		}
		for _, kv := range ev.Attributes {
			if strings.Contains(kv.Value.Emit(), secret) || strings.Contains(kv.Value.Emit(), "gw.example") {
				t.Errorf("event %s leaks the error text: %s", kv.Key, kv.Value.Emit())
			}
		}
	}
	for _, kv := range chat.Attributes {
		if strings.Contains(kv.Value.Emit(), secret) || strings.Contains(kv.Value.Emit(), "gw.example") {
			t.Errorf("attribute %s leaks the error text: %s", kv.Key, kv.Value.Emit())
		}
	}
}

// A base URL can carry the key in its path or query on a gateway, so only the
// host reaches the span.
func TestHook_ChatSpanRecordsHostOnly(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	meta := hooks.HookMeta{SessionID: "sess-host", AgentID: "agent-1", UserID: "u1"}
	base := "https://gw.example.com:8443/proxy/v1?token=sk-abcdef0123456789xyz"
	_, _ = h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{
		HookMeta: meta, Model: "claude-3", BaseURL: base,
	})
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: meta, Model: "claude-3", BaseURL: base, Duration: time.Second,
	})

	chat, ok := spanByName(endedStubs(sr), "gen_ai.chat")
	if !ok {
		t.Fatal("missing gen_ai.chat span")
	}
	if v, _ := attrValue(chat, "server.address"); v.AsString() != "gw.example.com:8443" {
		t.Errorf("server.address = %q, want the host only", v.AsString())
	}
	for _, kv := range chat.Attributes {
		if strings.Contains(kv.Value.Emit(), "sk-abcdef") || strings.Contains(kv.Value.Emit(), "/proxy/v1") {
			t.Errorf("attribute %s leaks the base URL: %s", kv.Key, kv.Value.Emit())
		}
	}
}

// driveToolCall runs one tool call through the hook with the given verdict.
func driveToolCall(h *Hook, sessionID string, post hooks.PostToolCallContext) {
	meta := hooks.HookMeta{SessionID: sessionID, AgentID: "agent-1", UserID: "u1"}
	_, _ = h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{HookMeta: meta, Model: "claude-3"})
	_, _ = h.OnPreToolCall(context.Background(), &hooks.PreToolCallContext{
		HookMeta: meta, ToolName: "bash", ToolCallID: "call-1",
		Arguments: map[string]any{"command": "grep -q needle haystack"},
	})
	post.HookMeta = meta
	post.ToolName = "bash"
	post.ToolCallID = "call-1"
	h.OnPostToolCall(context.Background(), &post)
}

func TestHook_ToolSpanErrorKind(t *testing.T) {
	exit := 2
	tests := []struct {
		name       string
		post       hooks.PostToolCallContext
		wantKind   string
		wantExit   int64 // -1 = attribute must be absent
		wantStatus codes.Code
	}{
		{
			// The tool worked and the command said no. Flagging that as a failed
			// span turns normal exploration into an error-rate spike (#1077).
			name:       "a nonzero command exit is not a span error",
			post:       hooks.PostToolCallContext{IsError: true, ErrorKind: ai.ToolErrorKindCommandNonzero, ExitCode: &exit},
			wantKind:   "command_nonzero",
			wantExit:   2,
			wantStatus: codes.Unset,
		},
		{
			name:       "a broken tool is a span error",
			post:       hooks.PostToolCallContext{IsError: true, ErrorKind: ai.ToolErrorKindTool},
			wantKind:   "tool_error",
			wantExit:   -1,
			wantStatus: codes.Error,
		},
		{
			// A caller that predates the split says nothing; the safe reading is
			// the default failure, never command_nonzero.
			name:       "an unclassified failure falls back to tool_error",
			post:       hooks.PostToolCallContext{IsError: true},
			wantKind:   "tool_error",
			wantExit:   -1,
			wantStatus: codes.Error,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, sr := newRecordingHook(t, false)
			driveToolCall(h, "sess-kind", tc.post)

			span, ok := spanByName(endedStubs(sr), "gen_ai.execute_tool")
			if !ok {
				t.Fatal("missing gen_ai.execute_tool span")
			}
			if v, ok := attrValue(span, "stella.tool.error_kind"); !ok || v.AsString() != tc.wantKind {
				t.Errorf("error_kind = %q (ok=%v), want %q", v.AsString(), ok, tc.wantKind)
			}
			if v, ok := attrValue(span, "error.type"); !ok || v.AsString() != tc.wantKind {
				t.Errorf("error.type = %q (ok=%v), want %q", v.AsString(), ok, tc.wantKind)
			}
			v, ok := attrValue(span, "stella.tool.exit_code")
			switch {
			case tc.wantExit < 0 && ok:
				t.Errorf("exit_code = %d, want no attribute", v.AsInt64())
			case tc.wantExit >= 0 && (!ok || v.AsInt64() != tc.wantExit):
				t.Errorf("exit_code = %d (ok=%v), want %d", v.AsInt64(), ok, tc.wantExit)
			}
			if span.Status.Code != tc.wantStatus {
				t.Errorf("status = %v, want %v", span.Status.Code, tc.wantStatus)
			}
		})
	}
}

func TestHook_ToolSpanSuccessCarriesNoErrorKind(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	driveToolCall(h, "sess-ok", hooks.PostToolCallContext{Result: "found"})

	span, ok := spanByName(endedStubs(sr), "gen_ai.execute_tool")
	if !ok {
		t.Fatal("missing gen_ai.execute_tool span")
	}
	if v, ok := attrValue(span, "stella.tool.error_kind"); ok {
		t.Errorf("successful tool span carries error_kind %q", v.AsString())
	}
	if span.Status.Code == codes.Error {
		t.Error("successful tool span marked as an error")
	}
}

// Retries live inside the provider SDK, below every span this hook owns, so
// the attempt count on the chat span is the only place they are visible.
func TestHook_ChatSpanRecordsProviderAttempts(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	meta := hooks.HookMeta{SessionID: "sess-retry", AgentID: "agent-1", UserID: "u1"}
	_, _ = h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{HookMeta: meta, Model: "claude-3"})
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: meta, Model: "claude-3", Duration: time.Second,
		TimeToFirstToken: 250 * time.Millisecond, Attempts: 3,
	})

	chat, ok := spanByName(endedStubs(sr), "gen_ai.chat")
	if !ok {
		t.Fatal("missing gen_ai.chat span")
	}
	if v, ok := attrValue(chat, "stella.llm.attempts"); !ok || v.AsInt64() != 3 {
		t.Errorf("attempts = %d (ok=%v), want 3", v.AsInt64(), ok)
	}
	if v, ok := attrValue(chat, "stella.llm.retry_count"); !ok || v.AsInt64() != 2 {
		t.Errorf("retry_count = %d (ok=%v), want 2", v.AsInt64(), ok)
	}
	if v, ok := attrValue(chat, "stella.llm.time_to_first_token_s"); !ok || v.AsFloat64() != 0.25 {
		t.Errorf("ttft = %v (ok=%v), want 0.25", v.AsFloat64(), ok)
	}
}

// A stream that never went over HTTP counted nothing; absent is not "one try".
func TestHook_ProviderToolSurfaceAndLogCorrelation(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(observability.NewTraceContextHandler(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h, sr := newRecordingHook(t, false)
	meta := hooks.HookMeta{SessionID: "surface", AgentID: "agent-uuid", UserID: "user", Channel: "web", BindingID: "binding"}
	pre := hooks.PreLLMCallContext{HookMeta: meta, CallID: "call", Model: "model", API: "provider", ToolDefinitions: []ai.ToolDefinition{{Name: "hidden"}}}
	if _, err := h.OnPreLLMCall(context.Background(), &pre); err != nil {
		t.Fatal(err)
	}
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: meta, CallID: "call", Model: "model", API: "provider",
		ProviderToolNames: []string{"bash", "code"}, CodeCatalogSize: 12,
	})
	chat, ok := spanByName(endedStubs(sr), "gen_ai.chat")
	if !ok {
		t.Fatal("missing gen_ai.chat span")
	}
	if got, ok := attrValue(chat, "gen_ai.request.tool_names"); !ok || got.AsStringSlice() == nil || strings.Join(got.AsStringSlice(), ",") != "bash,code" {
		t.Fatalf("provider tool names = %v (ok=%v)", got, ok)
	}
	if got, ok := attrValue(chat, "gen_ai.request.tool_count"); !ok || got.AsInt64() != 2 {
		t.Fatalf("provider tool count = %v (ok=%v)", got, ok)
	}
	if got, ok := attrValue(chat, "stella.code.catalog_size"); !ok || got.AsInt64() != 12 {
		t.Fatalf("catalog size = %v (ok=%v)", got, ok)
	}
	if !strings.Contains(logs.String(), `"trace_id"`) || !strings.Contains(logs.String(), `"span_id"`) {
		t.Fatalf("pre_llm_call was not correlated: %s", logs.String())
	}
}

func TestHook_ChatSpanOmitsUncountedAttempts(t *testing.T) {
	h, sr := newRecordingHook(t, false)
	meta := hooks.HookMeta{SessionID: "sess-noattempts", AgentID: "agent-1", UserID: "u1"}
	_, _ = h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{HookMeta: meta, Model: "claude-3"})
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{HookMeta: meta, Model: "claude-3"})

	chat, _ := spanByName(endedStubs(sr), "gen_ai.chat")
	if _, ok := attrValue(chat, "stella.llm.attempts"); ok {
		t.Error("attempts recorded when nothing counted them")
	}
}
