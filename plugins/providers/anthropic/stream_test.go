package anthropic

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestMapEventContentDelta(t *testing.T) {
	blockToID := make(map[int]string)
	event := sdk.MessageStreamEventUnion{
		Type: "content_block_delta",
		Delta: sdk.MessageStreamEventUnionDelta{
			Type: "text_delta",
			Text: "hello",
		},
	}
	events := mapEvent(event, blockToID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if _, ok := events[0].(ai.EventTextDelta); !ok {
		t.Fatalf("expected text delta")
	}
}

func TestMapEventMessageDeltaUsage(t *testing.T) {
	blockToID := make(map[int]string)
	event := sdk.MessageStreamEventUnion{
		Type: "message_delta",
		Delta: sdk.MessageStreamEventUnionDelta{
			StopReason: "end_turn",
		},
		Usage: sdk.MessageDeltaUsage{
			InputTokens:              8,
			OutputTokens:             2,
			CacheReadInputTokens:     5,
			CacheCreationInputTokens: 3,
		},
	}
	events := mapEvent(event, blockToID)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if _, ok := events[0].(ai.EventStop); !ok {
		t.Fatalf("expected stop event")
	}
	usage, ok := events[1].(ai.EventUsage)
	if !ok {
		t.Fatalf("expected usage event")
	}
	want := ai.Usage{InputTokens: 8, OutputTokens: 2, CacheRead: 5, CacheWrite: 3, TotalTokens: 18}
	if usage.Usage != want {
		t.Fatalf("usage = %+v, want %+v", usage.Usage, want)
	}
}

func TestMapEventToolCallDeltaCarriesID(t *testing.T) {
	blockToID := make(map[int]string)

	// Simulate content_block_start for a tool_use block at index 1.
	startEvent := sdk.MessageStreamEventUnion{
		Type:  "content_block_start",
		Index: 1,
		ContentBlock: sdk.ContentBlockStartEventContentBlockUnion{
			Type: "tool_use",
			ID:   "toolu_abc",
			Name: "read_file",
		},
	}
	events := mapEvent(startEvent, blockToID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event from start, got %d", len(events))
	}
	tc, ok := events[0].(ai.EventToolCallDelta)
	if !ok {
		t.Fatal("expected EventToolCallDelta from start")
	}
	if tc.ID != "toolu_abc" || tc.Name != "read_file" {
		t.Fatalf("unexpected start delta: %+v", tc)
	}

	// Simulate input_json_delta at the same index — ID must be carried forward.
	argEvent := sdk.MessageStreamEventUnion{
		Type:  "content_block_delta",
		Index: 1,
		Delta: sdk.MessageStreamEventUnionDelta{
			Type:        "input_json_delta",
			PartialJSON: `{"path":"/tmp"}`,
		},
	}
	events = mapEvent(argEvent, blockToID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event from delta, got %d", len(events))
	}
	tc, ok = events[0].(ai.EventToolCallDelta)
	if !ok {
		t.Fatal("expected EventToolCallDelta from argument delta")
	}
	if tc.ID != "toolu_abc" {
		t.Fatalf("expected ID 'toolu_abc' on argument delta, got %q", tc.ID)
	}
	if tc.Arguments != `{"path":"/tmp"}` {
		t.Fatalf("unexpected arguments: %q", tc.Arguments)
	}
}
