package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
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
	h, err := runner.Run(context.Background(), messages, func(e LoopEvent) {
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

func TestRunToolCallLimitResetsBetweenRuns(t *testing.T) {
	var executed atomic.Int32
	var definitionHidden atomic.Bool

	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		// Ask for a third invocation after two successful results so the runtime
		// limit, rather than model cooperation, is exercised.
		resultCount := 0
		for _, message := range request.Messages {
			if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "limited_tool" {
				resultCount++
			}
		}
		if resultCount >= 2 {
			hasLimitedTool := false
			for _, definition := range request.Tools {
				if definition.Name == "limited_tool" {
					hasLimitedTool = true
				}
			}
			if !hasLimitedTool {
				definitionHidden.Store(true)
			}
		}

		out := providers.NewChannelEventStream(8)
		go func() {
			if resultCount < 3 {
				out.Emit(ai.EventToolCallDelta{
					ID:        fmt.Sprintf("call_%d", resultCount+1),
					Name:      "limited_tool",
					Arguments: "{}",
				})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}

	runner := newTestRunner(stream, WithToolCallLimit("limited_tool", 2))
	runner.tools = ToolSet{
		"limited_tool": func(_ context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
			executed.Add(1)
			return []ai.ContentBlock{ai.TextContent{Text: "tool result"}}, nil
		},
	}
	runner.toolDefs = []ai.ToolDefinition{{Name: "limited_tool"}}

	for runNumber := 1; runNumber <= 2; runNumber++ {
		definitionHidden.Store(false)
		history, _, err := collectEvents(runner, []ai.Message{ai.UserMessage{Content: "go"}})
		if err != nil {
			t.Fatalf("run %d: %v", runNumber, err)
		}
		if got, want := executed.Load(), int32(runNumber*2); got != want {
			t.Fatalf("executed after run %d = %d, want %d", runNumber, got, want)
		}
		if !definitionHidden.Load() {
			t.Fatalf("run %d still exposed exhausted tool definition", runNumber)
		}

		var results []ai.ToolResultMessage
		for _, message := range history {
			if result, ok := message.(ai.ToolResultMessage); ok {
				results = append(results, result)
			}
		}
		if len(results) != 3 {
			t.Fatalf("run %d tool results = %d, want 3", runNumber, len(results))
		}
		if results[0].IsError || results[1].IsError {
			t.Fatalf("run %d first two calls should succeed: %+v", runNumber, results)
		}
		if !results[2].IsError || !strings.Contains(ai.FlattenText(results[2].Content), "at most 2 times") {
			t.Fatalf("run %d third result = %+v, want limit error", runNumber, results[2])
		}
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
