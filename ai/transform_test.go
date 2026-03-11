package ai

import "testing"

func TestTransformMessagesAddsSyntheticToolResultForOrphanToolCall(t *testing.T) {
	input := []Message{
		UserMessage{Content: "hi"},
		AssistantMessage{Content: []ContentBlock{
			ToolCall{ID: "tool-1", Name: "lookup"},
		}},
	}

	out := TransformMessages(input)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}

	toolMsg, ok := out[2].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected synthetic tool result message at index 2")
	}
	if !toolMsg.IsError {
		t.Fatalf("expected synthetic tool result to be marked error")
	}
	if toolMsg.ToolCallID != "tool-1" {
		t.Fatalf("expected toolCallID tool-1, got %q", toolMsg.ToolCallID)
	}
}

func TestTransformMessagesKeepsExistingToolResult(t *testing.T) {
	input := []Message{
		AssistantMessage{Content: []ContentBlock{
			ToolCall{ID: "tool-1", Name: "lookup"},
		}},
		ToolResultMessage{ToolCallID: "tool-1", ToolName: "lookup"},
	}

	out := TransformMessages(input)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
}
