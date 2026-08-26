package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

func defaultFakeStream() providers.StreamFunc {
	return func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventTextDelta{Text: "response"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
}

func newTestRunner(stream providers.StreamFunc, opts ...Option) *Runner {
	runner, _ := NewRunner(RunnerConfig{
		Stream: stream,
		Model:  ai.Model{API: "fake", Name: "stub"},
	}, opts...)
	return runner
}

func collectEvents(runner *Runner, messages []ai.Message) ([]ai.Message, []LoopEvent, error) {
	var events []LoopEvent
	h, err := runner.RunWithActiveStart(context.Background(), messages, 0, func(e LoopEvent) {
		events = append(events, e)
	})
	return h, events, err
}

func countEvents[T LoopEvent](events []LoopEvent) int {
	n := 0
	for _, e := range events {
		if _, ok := e.(T); ok {
			n++
		}
	}
	return n
}

func toolCallIDs(blocks []ai.ContentBlock) []string {
	ids := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if call, ok := block.(ai.ToolCall); ok {
			ids = append(ids, call.ID)
		}
	}
	return ids
}

func TestRunEmitsStreamingEvents(t *testing.T) {
	runner := newTestRunner(defaultFakeStream())
	history, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "hello"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected history len 2, got %d", len(history))
	}

	// Verify lifecycle: AgentStarted, TurnStarted, AssistantStarted, AssistantDelta(s), AssistantFinished, TurnFinished, AgentFinished
	if _, ok := events[0].(AgentStarted); !ok {
		t.Fatalf("expected AgentStarted first, got %T", events[0])
	}
	if _, ok := events[1].(TurnStarted); !ok {
		t.Fatalf("expected TurnStarted second, got %T", events[1])
	}
	if _, ok := events[2].(AssistantStarted); !ok {
		t.Fatalf("expected AssistantStarted third, got %T", events[2])
	}

	// Should have deltas for TextDelta and Stop events
	if countEvents[AssistantDelta](events) < 1 {
		t.Fatalf("expected at least 1 AssistantDelta")
	}

	// Last 3 should be AssistantFinished, TurnFinished, AgentFinished
	n := len(events)
	if _, ok := events[n-3].(AssistantFinished); !ok {
		t.Fatalf("expected AssistantFinished at n-3, got %T", events[n-3])
	}
	if _, ok := events[n-2].(TurnFinished); !ok {
		t.Fatalf("expected TurnFinished at n-2, got %T", events[n-2])
	}
	if _, ok := events[n-1].(AgentFinished); !ok {
		t.Fatalf("expected AgentFinished at n-1, got %T", events[n-1])
	}

	// Verify final message text
	finished := events[n-3].(AssistantFinished)
	if len(finished.Message.Content) == 0 {
		t.Fatalf("expected content in final message")
	}
	tc, ok := finished.Message.Content[0].(ai.TextContent)
	if !ok || tc.Text != "response" {
		t.Fatalf("expected text 'response', got %v", finished.Message.Content[0])
	}
}

func TestRunPreservesToolCallOrder(t *testing.T) {
	wantIDs := []string{"call-e", "call-d", "call-c", "call-b", "call-a"}
	providerCalls := 0
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		providerCalls++
		providerCall := providerCalls
		out := providers.NewChannelEventStream(32)
		go func() {
			if providerCall == 1 {
				for _, id := range wantIDs {
					out.Emit(ai.EventToolCallDelta{ID: id, Name: "ordered"})
				}
				// Argument chunks may interleave without changing first-seen order.
				for i := len(wantIDs) - 1; i >= 0; i-- {
					out.Emit(ai.EventToolCallDelta{ID: wantIDs[i], Arguments: `{"index":`})
				}
				for i := len(wantIDs) - 1; i >= 0; i-- {
					out.Emit(ai.EventToolCallDelta{ID: wantIDs[i], Arguments: fmt.Sprintf("%d}", i)})
				}
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream)
	var executed []string
	runner.tools = ToolSet{"ordered": func(_ context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
		executed = append(executed, call.ID)
		return []ai.ContentBlock{ai.TextContent{Text: call.ID}}, nil
	}}

	history, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(executed, wantIDs) {
		t.Fatalf("execution order = %v, want %v", executed, wantIDs)
	}

	var partialIDs, finalIDs, startedIDs, finishedIDs []string
	for _, event := range events {
		switch e := event.(type) {
		case AssistantDelta:
			if _, ok := e.Event.(ai.EventStop); ok && len(partialIDs) == 0 {
				partialIDs = toolCallIDs(e.Message.Content)
			}
		case AssistantFinished:
			if ids := toolCallIDs(e.Message.Content); len(ids) > 0 {
				finalIDs = ids
			}
		case ToolStarted:
			startedIDs = append(startedIDs, e.ToolCall.ID)
		case ToolFinished:
			finishedIDs = append(finishedIDs, e.Result.ToolCallID)
		}
	}
	for label, got := range map[string][]string{
		"partial":  partialIDs,
		"final":    finalIDs,
		"started":  startedIDs,
		"finished": finishedIDs,
	} {
		if !slices.Equal(got, wantIDs) {
			t.Errorf("%s order = %v, want %v", label, got, wantIDs)
		}
	}

	if len(history) != len(wantIDs)+3 {
		t.Fatalf("history length = %d, want %d", len(history), len(wantIDs)+3)
	}
	assistant, ok := history[1].(ai.AssistantMessage)
	if !ok || !slices.Equal(toolCallIDs(assistant.Content), wantIDs) {
		t.Fatalf("persisted assistant order = %v, want %v", toolCallIDs(assistant.Content), wantIDs)
	}
	for i, wantID := range wantIDs {
		result, ok := history[i+2].(ai.ToolResultMessage)
		if !ok || result.ToolCallID != wantID {
			t.Fatalf("persisted result %d = %#v, want %q", i, history[i+2], wantID)
		}
	}
}

func TestRunKeepsCompletedToolResultWhenLaterCallFails(t *testing.T) {
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventToolCallDelta{ID: "1", Name: "ok"})
			out.Emit(ai.EventToolCallDelta{ID: "2", Name: "ok"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			out.Finish(nil)
		}()
		return out, nil
	}
	runner := newTestRunner(stream)
	runner.tools = ToolSet{"ok": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "done"}}, nil
	}}
	runner.toolLifecycle = &ToolLifecycle{BeforeCall: func(_ context.Context, call ToolCallContext) (ToolCallMutation, error) {
		if call.ToolCallID == "2" {
			return ToolCallMutation{}, fmt.Errorf("second call failed")
		}
		return ToolCallMutation{}, nil
	}}

	history, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err == nil || err.Error() != "second call failed" {
		t.Fatalf("error = %v, want second call failed", err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d, want user + assistant tool calls + completed result", len(history))
	}
	result, ok := history[2].(ai.ToolResultMessage)
	if !ok || result.ToolCallID != "1" || ai.FlattenText(result.Content) != "done" {
		t.Fatalf("persistable completed result = %#v, want call 1", history[2])
	}
	var finished []string
	for _, event := range events {
		if event, ok := event.(ToolFinished); ok {
			finished = append(finished, event.Result.ToolCallID)
		}
	}
	if !slices.Equal(finished, []string{"1"}) {
		t.Fatalf("ToolFinished calls = %v, want [1]", finished)
	}
}

func TestBuildPartialUsesFirstSeenToolCallOrder(t *testing.T) {
	calls := map[string]ai.ToolCall{
		"a": {ID: "a", Name: "first"},
		"b": {ID: "b", Name: "second"},
		"c": {ID: "c", Name: "third"},
	}
	partial := buildPartial(ai.AssistantMessage{}, "", "", calls, []string{"c", "a", "b"})
	if got, want := toolCallIDs(partial.Content), []string{"c", "a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("partial order = %v, want %v", got, want)
	}
}

func TestRunStreamingDeltasCarryPartial(t *testing.T) {
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventTextDelta{Text: "hello "})
			out.Emit(ai.EventTextDelta{Text: "world"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream)
	_, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the second text delta — should have accumulated text
	deltaCount := 0
	for _, e := range events {
		d, ok := e.(AssistantDelta)
		if !ok {
			continue
		}
		if _, isText := d.Event.(ai.EventTextDelta); !isText {
			continue
		}
		deltaCount++
		if deltaCount == 2 {
			if len(d.Message.Content) == 0 {
				t.Fatalf("expected partial content in second delta")
			}
			tc, ok := d.Message.Content[0].(ai.TextContent)
			if !ok || tc.Text != "hello world" {
				t.Fatalf("expected accumulated text 'hello world', got %v", d.Message.Content)
			}
		}
	}
	if deltaCount < 2 {
		t.Fatalf("expected at least 2 text deltas, got %d", deltaCount)
	}
}

type modelOverrideHook struct{ model string }

func (h modelOverrideHook) Name() string { return "model-override" }
func (modelOverrideHook) Priority() int  { return 0 }
func (h modelOverrideHook) OnPreLLMCall(context.Context, *hooks.PreLLMCallContext) (hooks.PreLLMCallResult, error) {
	return hooks.PreLLMCallResult{Model: &h.model}, nil
}

func TestPreLLMModelOverrideFailsClosedForImageCapability(t *testing.T) {
	var providerContexts []ai.Context
	var toolCapability ai.ImageCapability
	providerCalls := 0
	stream := func(_ context.Context, model ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if model.Name != "unknown-target" || model.ImageCapability() != ai.ImageUnknown {
			t.Fatalf("effective model = %#v, want unknown override", model)
		}
		providerContexts = append(providerContexts, aiCtx)
		providerCalls++
		out := providers.NewChannelEventStream(4)
		go func() {
			if providerCalls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "tool", Name: "check"})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
			}
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
	runner, err := NewRunner(RunnerConfig{
		Stream: stream,
		Model:  supportedModel(),
		Tools: ToolSet{"check": func(ctx context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
			toolCapability = tools.ParentImageCapabilityFromContext(ctx)
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		}},
	}, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{modelOverrideHook{model: "unknown-target"}}), hooks.HookMeta{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}}}}, 0, nil); err != nil {
		t.Fatal(err)
	}
	if toolCapability != ai.ImageUnknown {
		t.Fatalf("tool capability = %v, want ImageUnknown after model-name override", toolCapability)
	}
	for call, aiCtx := range providerContexts {
		for _, msg := range aiCtx.Messages {
			if ai.HasImage(messageBlocks(msg)) {
				t.Fatalf("provider call %d received ImageContent after model override", call+1)
			}
		}
	}
}

func TestCanonicalImagePolicyIsExplicit(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts []Option
		want bool
	}{
		{name: "default remains non-canonical", want: false},
		{name: "complete canonical policy", opts: []Option{withTestCanonicalImages(func(context.Context, string) (ai.ImageContent, error) {
			return ai.ImageContent{Data: "pixels", MimeType: "image/png"}, nil
		})}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			seen := true
			stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
				calls++
				out := providers.NewChannelEventStream(4)
				go func() {
					if calls == 1 {
						out.Emit(ai.EventToolCallDelta{ID: "check", Name: "check"})
					} else {
						out.Emit(ai.EventTextDelta{Text: "done"})
					}
					out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
					out.Finish(nil)
				}()
				return out, nil
			}
			runner := newTestRunner(stream, tt.opts...)
			runner.tools["check"] = func(ctx context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
				seen = tools.ImageResultModeFromContext(ctx) == tools.ImageResultCanonical
				return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
			}
			if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
				t.Fatal(err)
			}
			if seen != tt.want {
				t.Fatalf("canonical image mode = %t, want %t", seen, tt.want)
			}
		})
	}
}

func TestRunMultiTurnLoop(t *testing.T) {
	var callCount atomic.Int32

	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		n := callCount.Add(1)
		go func() {
			if n <= 2 {
				out.Emit(ai.EventToolCallDelta{ID: fmt.Sprintf("call_%d", n), Name: "test_tool", Arguments: "{}"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream)
	// Override tools after construction — tests use the unexported run() path via Runner.
	runner.tools = ToolSet{
		"test_tool": func(_ context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "tool result"}}, nil
		},
	}

	history, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// history: user + (assistant + tool_result) * 2 + assistant = 6
	if len(history) != 6 {
		t.Fatalf("expected history len 6, got %d", len(history))
	}

	if countEvents[TurnStarted](events) != 3 {
		t.Fatalf("expected 3 TurnStarted, got %d", countEvents[TurnStarted](events))
	}
	if countEvents[TurnFinished](events) != 3 {
		t.Fatalf("expected 3 TurnFinished, got %d", countEvents[TurnFinished](events))
	}
	if countEvents[AssistantStarted](events) != 3 {
		t.Fatalf("expected 3 AssistantStarted, got %d", countEvents[AssistantStarted](events))
	}
	if countEvents[AssistantFinished](events) != 3 {
		t.Fatalf("expected 3 AssistantFinished, got %d", countEvents[AssistantFinished](events))
	}
}

func TestRunErrorStopReasonSurfacesError(t *testing.T) {
	// A provider may signal StopReason=Error without any EventError detail;
	// the loop must still fail loudly instead of finishing cleanly.
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventError{Err: nil})
			out.Emit(ai.EventStop{Reason: ai.StopReasonError})
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream)
	history, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err == nil {
		t.Fatal("expected error for StopReason=Error with no detail, got nil")
	}
	if !strings.Contains(err.Error(), "provider:") {
		t.Fatalf("expected provider error, got %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected history len 1 (failed turn not appended), got %d", len(history))
	}
	if countEvents[AgentErrored](events) != 1 {
		t.Fatalf("expected 1 AgentErrored, got %d", countEvents[AgentErrored](events))
	}
}

func TestRunInterruptStopsLoop(t *testing.T) {
	var callCount atomic.Int32
	interrupt := make(chan struct{})

	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		n := callCount.Add(1)
		go func() {
			out.Emit(ai.EventToolCallDelta{ID: "call_1", Name: "test_tool", Arguments: "{}"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			out.Finish(nil)
		}()
		if n == 1 {
			close(interrupt)
		}
		return out, nil
	}

	runner := newTestRunner(stream, WithInterrupt(interrupt))
	runner.tools = ToolSet{
		"test_tool": func(_ context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		},
	}

	history, _, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected history len 2, got %d", len(history))
	}
}

func TestContinueRequiresValidTail(t *testing.T) {
	runner, _ := NewRunner(RunnerConfig{Stream: defaultFakeStream()})
	_, err := runner.Continue(context.Background(), []ai.Message{ai.AssistantMessage{}}, nil)
	if err == nil {
		t.Fatalf("expected tail validation error")
	}
}

func TestRunEmptyTruncatedTurnFailsInsteadOfFinishingSilently(t *testing.T) {
	// A reasoning model can spend its whole output budget thinking and return
	// nothing: finish_reason=length, no text, no tool call. Reported as a clean
	// finish it is indistinguishable from a model that chose to do nothing, and
	// on the Terminal-Bench baseline that silently lost 13 trials.
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventStop{Reason: ai.StopReasonLength})
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream)
	_, events, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err == nil {
		t.Fatal("an empty truncated turn finished cleanly")
	}
	if !strings.Contains(err.Error(), "output token limit") {
		t.Fatalf("error does not name the cause: %v", err)
	}
	if countEvents[AgentErrored](events) != 1 {
		t.Fatalf("expected 1 AgentErrored, got %d", countEvents[AgentErrored](events))
	}
}

func TestRunTruncatedTurnWithContentStaysAFinish(t *testing.T) {
	// Scoped: a truncated reply that carried text is still usable, and failing
	// it would throw away a partial answer the caller can read.
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventTextDelta{Text: "half an ans"})
			out.Emit(ai.EventStop{Reason: ai.StopReasonLength})
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream)
	history, _, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
	if err != nil {
		t.Fatalf("a truncated reply with content must still finish: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected the partial answer in history, got %d messages", len(history))
	}
}

// attemptRecorder captures the PostLLMCall telemetry the trace hook consumes.
type attemptRecorder struct{ last hooks.PostLLMCallContext }

func (*attemptRecorder) Name() string  { return "attempt-recorder" }
func (*attemptRecorder) Priority() int { return 0 }
func (r *attemptRecorder) OnPostLLMCall(_ context.Context, hctx *hooks.PostLLMCallContext) {
	r.last = *hctx
}

func TestLoopReportsProviderAttempts(t *testing.T) {
	rec := &attemptRecorder{}
	// Stand in for a provider SDK that retried once inside a single Stream call.
	stream := func(ctx context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		req := ai.ModelRequestFrom(ctx)
		if req == nil {
			t.Error("the loop did not scope the stream context to a model request")
		} else {
			req.NextAttempt()
			req.NextAttempt()
		}
		return defaultFakeStream()(ctx, ai.Model{}, ai.Context{}, ai.StreamOptions{})
	}
	runner := newTestRunner(stream, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{rec}), hooks.HookMeta{SessionID: "s1"}))

	if _, _, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if rec.last.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", rec.last.Attempts)
	}
}
