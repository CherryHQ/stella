package openairesponse

import (
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"

	"github.com/CherryHQ/stella/pkg/ai"
)

func mapEventForTest(t *testing.T, event responses.ResponseStreamEventUnion, state *streamState) []ai.AssistantEvent {
	t.Helper()
	events, err := state.mapEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestMapEventTextDelta(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.output_text.delta",
		Delta: responses.ResponseStreamEventUnionDelta{
			OfString: "hello",
		},
	}
	events := mapEventForTest(t, event, newStreamState())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	td, ok := events[0].(ai.EventTextDelta)
	if !ok {
		t.Fatalf("expected EventTextDelta, got %T", events[0])
	}
	if td.Text != "hello" {
		t.Fatalf("expected text 'hello', got %q", td.Text)
	}
}

func TestMapEventFunctionCallArgumentsDelta(t *testing.T) {
	// Simulate the real flow: output_item.added registers the item_id → call_id mapping,
	// then arguments.delta uses item_id which gets resolved to call_id.
	m := newStreamState()
	m.itemToCall["fc_0"] = "call_0"

	event := responses.ResponseStreamEventUnion{
		Type:   "response.function_call_arguments.delta",
		ItemID: "fc_0",
		Delta: responses.ResponseStreamEventUnionDelta{
			OfString: `{"q":"test"}`,
		},
	}
	events := mapEventForTest(t, event, m)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	tc, ok := events[0].(ai.EventToolCallDelta)
	if !ok {
		t.Fatalf("expected EventToolCallDelta, got %T", events[0])
	}
	if tc.ID != "call_0" || tc.Arguments != `{"q":"test"}` {
		t.Fatalf("unexpected tool call delta: %+v", tc)
	}
}

func TestMapEventOutputItemAddedFunctionCall(t *testing.T) {
	m := newStreamState()
	event := responses.ResponseStreamEventUnion{
		Type: "response.output_item.added",
		Item: responses.ResponseOutputItemUnion{
			ID:     "fc_1",
			Type:   "function_call",
			CallID: "call_1",
			Name:   "lookup",
		},
	}
	events := mapEventForTest(t, event, m)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	tc, ok := events[0].(ai.EventToolCallDelta)
	if !ok {
		t.Fatalf("expected EventToolCallDelta, got %T", events[0])
	}
	if tc.ID != "call_1" || tc.Name != "lookup" {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
	// Verify the item_id → call_id mapping was recorded.
	if m.itemToCall["fc_1"] != "call_1" {
		t.Fatalf("expected item_id mapping fc_1→call_1, got %q", m.itemToCall["fc_1"])
	}
}

func TestMapEventCompleted(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.completed",
		Response: responses.Response{
			Status: responses.ResponseStatusCompleted,
			Usage: responses.ResponseUsage{
				InputTokens:  10,
				OutputTokens: 5,
				TotalTokens:  15,
				InputTokensDetails: responses.ResponseUsageInputTokensDetails{
					CachedTokens: 7,
				},
			},
		},
	}
	events := mapEventForTest(t, event, newStreamState())
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	usage, ok := events[0].(ai.EventUsage)
	if !ok {
		t.Fatalf("expected EventUsage, got %T", events[0])
	}
	// input_tokens is 10 with 7 of them cached, so only 3 is billable input.
	wantUsage := ai.Usage{InputTokens: 3, OutputTokens: 5, CacheRead: 7, TotalTokens: 15}
	if usage.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", usage.Usage, wantUsage)
	}
	stop, ok := events[1].(ai.EventStop)
	if !ok {
		t.Fatalf("expected EventStop, got %T", events[1])
	}
	if stop.Reason != ai.StopReasonStop {
		t.Fatalf("expected stop reason 'stop', got %q", stop.Reason)
	}
}

func TestMapEventFailed(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.failed",
	}
	events := mapEventForTest(t, event, newStreamState())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	stop, ok := events[0].(ai.EventStop)
	if !ok {
		t.Fatalf("expected EventStop, got %T", events[0])
	}
	if stop.Reason != ai.StopReasonError {
		t.Fatalf("expected stop reason error, got %q", stop.Reason)
	}
}

func TestMapEventFunctionCallFlow(t *testing.T) {
	// Simulate the full function call streaming sequence:
	// 1. output_item.added (registers item_id → call_id)
	// 2. function_call_arguments.delta (uses item_id, resolved to call_id)
	m := newStreamState()

	// Step 1: output_item.added
	added := responses.ResponseStreamEventUnion{
		Type: "response.output_item.added",
		Item: responses.ResponseOutputItemUnion{
			ID:     "fc_abc",
			Type:   "function_call",
			CallID: "call_abc",
			Name:   "bash",
		},
	}
	events := mapEventForTest(t, added, m)
	if len(events) != 1 {
		t.Fatalf("step 1: expected 1 event, got %d", len(events))
	}
	tc := events[0].(ai.EventToolCallDelta)
	if tc.ID != "call_abc" || tc.Name != "bash" {
		t.Fatalf("step 1: unexpected: %+v", tc)
	}

	// Step 2: arguments delta (item_id=fc_abc should resolve to call_abc)
	delta := responses.ResponseStreamEventUnion{
		Type:   "response.function_call_arguments.delta",
		ItemID: "fc_abc",
		Delta: responses.ResponseStreamEventUnionDelta{
			OfString: `{"command":"ls"}`,
		},
	}
	events = mapEventForTest(t, delta, m)
	if len(events) != 1 {
		t.Fatalf("step 2: expected 1 event, got %d", len(events))
	}
	tc = events[0].(ai.EventToolCallDelta)
	if tc.ID != "call_abc" {
		t.Fatalf("step 2: expected ID call_abc, got %q", tc.ID)
	}
	if tc.Arguments != `{"command":"ls"}` {
		t.Fatalf("step 2: unexpected arguments: %q", tc.Arguments)
	}
}

func TestMapEventIncomplete(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.incomplete",
	}
	events := mapEventForTest(t, event, newStreamState())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	stop, ok := events[0].(ai.EventStop)
	if !ok {
		t.Fatalf("expected EventStop, got %T", events[0])
	}
	if stop.Reason != ai.StopReasonLength {
		t.Fatalf("expected stop reason length, got %q", stop.Reason)
	}
}

func TestSnapshotsRecoverTextWithoutDuplication(t *testing.T) {
	for _, kind := range []string{"output_text", "refusal"} {
		t.Run(kind, func(t *testing.T) {
			state := newStreamState()
			first := mapEventForTest(t, responses.ResponseStreamEventUnion{Type: "response." + kind + ".delta", ItemID: "msg_1", Delta: responses.ResponseStreamEventUnionDelta{OfString: "hel"}}, state)
			done := responses.ResponseStreamEventUnion{Type: "response." + kind + ".done", ItemID: "msg_1", Text: "hello", Refusal: "hello"}
			tail := mapEventForTest(t, done, state)
			repeated := mapEventForTest(t, done, state)
			if len(first) != 1 || len(tail) != 1 || len(repeated) != 0 || first[0].(ai.EventTextDelta).Text+tail[0].(ai.EventTextDelta).Text != "hello" {
				t.Fatalf("unexpected text events: %v %v %v", first, tail, repeated)
			}
		})
	}
}

func TestConflictingSnapshotsFailWithoutLeakingArguments(t *testing.T) {
	state := newStreamState()
	item := responses.ResponseOutputItemUnion{ID: "fc_1", Type: "function_call", CallID: "call_1", Name: "lookup", Arguments: `{"secret":"private-value"}`}
	mapEventForTest(t, responses.ResponseStreamEventUnion{Type: "response.output_item.added", Item: item}, state)
	item.Arguments = `{"secret":"different-private-value"}`
	if _, err := state.mapEvent(responses.ResponseStreamEventUnion{Type: "response.output_item.done", Item: item}); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("expected redacted conflict error, got %v", err)
	}
	if _, err := state.mapEvent(responses.ResponseStreamEventUnion{Type: "response.output_text.delta", ItemID: "msg", Delta: responses.ResponseStreamEventUnionDelta{OfString: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.mapEvent(responses.ResponseStreamEventUnion{Type: "response.output_text.done", ItemID: "msg", Text: "goodbye"}); err == nil {
		t.Fatal("conflicting text was accepted")
	}
}
