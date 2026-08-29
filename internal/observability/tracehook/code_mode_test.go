package tracehook_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/CherryHQ/stella/internal/observability/tracehook"
	"github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

func TestCodeChildTraceUsesNestedAuditIDAndRedactsIO(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	streamCalls := 0
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		streamCalls++
		out := providers.NewChannelEventStream(2)
		go func() {
			if streamCalls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: "code", Arguments: `{"code":"return await tools.invoke('effect', { token: 'sk-proj-abcdef1234567890' });"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Stream: stream,
		Tools: agent.ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "token=sk-proj-abcdef1234567890"}}, nil
		}},
		ToolDefinitions: []ai.ToolDefinition{{Name: "effect"}},
	}, agent.WithHooks(hooks.NewHookSet([]hooks.HookPlugin{tracehook.New(false, false)}), hooks.HookMeta{AgentID: "agent", SessionID: "session", UserID: "user"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
		t.Fatal(err)
	}

	got := logs.String()
	if strings.Count(got, `"call_id":"outer:1"`) != 2 || strings.Count(got, `"tool":"code"`) != 2 || !strings.Contains(got, `"tool":"effect"`) || !strings.Contains(got, `"duration"`) {
		t.Fatalf("nested trace records = %s", got)
	}
	if strings.Contains(got, "sk-proj-abcdef1234567890") || strings.Contains(got, `"input"`) || strings.Contains(got, `"result"`) {
		t.Fatalf("trace IO was exported without opt-in: %s", got)
	}
}

func TestCodeOuterCallHasNestedTraceAndAudit(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	hook := tracehook.New(true, false)
	defer func() { _ = hook.Close() }()
	streamCalls := 0
	var providerTools []string
	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if streamCalls == 0 {
			for _, tool := range request.Tools {
				providerTools = append(providerTools, tool.Name)
			}
		}
		streamCalls++
		out := providers.NewChannelEventStream(2)
		go func() {
			if streamCalls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: "code", Arguments: `{"code":"await tools.invoke('one'); return await tools.invoke('two');"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}
	runner, err := agent.NewRunner(agent.RunnerConfig{
		Model:  ai.Model{Name: "model"},
		Stream: stream,
		Tools: agent.ToolSet{
			"one": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				return []ai.ContentBlock{ai.TextContent{Text: "one"}}, nil
			},
			"two": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				return []ai.ContentBlock{ai.TextContent{Text: "two"}}, nil
			},
		},
		ToolDefinitions: []ai.ToolDefinition{{Name: "one"}, {Name: "two"}},
	}, agent.WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{AgentID: "agent", SessionID: "session"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
		t.Fatal(err)
	}
	hook.OnPostAgentCall(context.Background(), &hooks.PostAgentCallContext{HookMeta: hooks.HookMeta{AgentID: "agent", SessionID: "session"}})

	spans := tracetest.SpanStubsFromReadOnlySpans(recorder.Ended())
	turns := make([]tracetest.SpanStub, 0, 2)
	var outer, chat tracetest.SpanStub
	children := make([]tracetest.SpanStub, 0, 2)
	for _, span := range spans {
		switch span.Name {
		case "agent.turn":
			turns = append(turns, span)
		case "chat model":
			chat = span
		case "execute_tool one", "execute_tool two", "execute_tool code", "execute_tool bash":
			name := ""
			for _, kv := range span.Attributes {
				if string(kv.Key) == "gen_ai.tool.name" {
					name = kv.Value.AsString()
				}
			}
			if name == "code" {
				outer = span
			} else {
				children = append(children, span)
			}
		}
	}
	if len(turns) == 0 || !outer.SpanContext.IsValid() || !chat.SpanContext.IsValid() || len(children) != 2 {
		t.Fatalf("spans = %#v", spans)
	}
	var seenProviderTools []string
	for _, kv := range chat.Attributes {
		if string(kv.Key) == "stella.llm.provider_tool_names" {
			seenProviderTools = kv.Value.AsStringSlice()
		}
	}
	if strings.Join(seenProviderTools, ",") != strings.Join(providerTools, ",") {
		t.Fatalf("provider tools in span=%v, provider received=%v", seenProviderTools, providerTools)
	}
	var turn tracetest.SpanStub
	for _, candidate := range turns {
		if outer.Parent.SpanID() == candidate.SpanContext.SpanID() {
			turn = candidate
			break
		}
	}
	if !turn.SpanContext.IsValid() {
		t.Fatalf("code parent=%v did not match a turn: %#v", outer.Parent.SpanID(), turns)
	}
	var childCount, childErrorCount int64
	var failureClass string
	for _, kv := range outer.Attributes {
		switch string(kv.Key) {
		case "stella.tool.child_count":
			childCount = kv.Value.AsInt64()
		case "stella.tool.child_error_count":
			childErrorCount = kv.Value.AsInt64()
		case "stella.tool.failure_class":
			failureClass = kv.Value.AsString()
		}
	}
	if childCount != 2 || childErrorCount != 0 || failureClass != "" {
		t.Fatalf("code audit attributes count=%d errors=%d class=%q", childCount, childErrorCount, failureClass)
	}
	for _, child := range children {
		if child.Parent.SpanID() != outer.SpanContext.SpanID() {
			t.Fatalf("child parent=%v want code=%v", child.Parent.SpanID(), outer.SpanContext.SpanID())
		}
		if turn.EndTime.Before(child.EndTime) {
			t.Fatalf("turn ended before child: turn=%s child=%s", turn.EndTime, child.EndTime)
		}
	}
}
