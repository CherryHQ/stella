package runtime

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestEncodeEventRoundTrip(t *testing.T) {
	in := Event{
		Text: "hello",
		ToolUse: &ToolUseEvent{
			ID: "t-1", Tool: "bash", Status: "done", Input: "ls",
			Arguments: map[string]any{"cmd": "ls"},
		},
		Step: &StepEvent{Kind: "finish"},
		Err:  errors.New("boom"),
	}
	raw := EncodeEvent(in)
	out, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "hello" || out.ToolUse == nil || out.ToolUse.ID != "t-1" ||
		out.Step == nil || out.Step.Kind != "finish" || out.Err == nil || out.Err.Error() != "boom" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestEncodeEventDropsStore(t *testing.T) {
	in := Event{Store: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "x"}}}}
	raw := EncodeEvent(in)
	out, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Store != nil {
		t.Fatal("store must not round-trip")
	}
}
