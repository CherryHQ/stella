package openai

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestConvertMessagesEmpty(t *testing.T) {
	ctx := ai.Context{}
	msgs := convertMessages(ctx)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}

func TestConvertMessagesWithSystem(t *testing.T) {
	ctx := ai.Context{
		System:   "you are helpful",
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0].OfSystem == nil {
		t.Error("expected first message to be system")
	}
}

func TestConvertMessagesUserText(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{ai.UserMessage{Content: "hello"}},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].OfUser == nil {
		t.Fatal("expected OfUser")
	}
}

func TestConvertMessagesAssistantTextOnly(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "response"}},
			},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].OfAssistant == nil {
		t.Fatal("expected OfAssistant")
	}
}

func TestConvertMessagesAssistantWithToolCall(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "checking"},
					ai.ToolCall{ID: "call_1", Name: "bash", Arguments: map[string]any{"cmd": "ls"}},
				},
			},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].OfAssistant == nil {
		t.Fatal("expected OfAssistant")
	}
	if len(msgs[0].OfAssistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msgs[0].OfAssistant.ToolCalls))
	}
	if msgs[0].OfAssistant.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("tool name = %q, want bash", msgs[0].OfAssistant.ToolCalls[0].Function.Name)
	}
}

func TestConvertMessagesToolResult(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.ToolResultMessage{
				ToolCallID: "call_1",
				Content:    []ai.ContentBlock{ai.TextContent{Text: "output"}},
			},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].OfTool == nil {
		t.Fatal("expected OfTool")
	}
	if msgs[0].OfTool.ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q, want call_1", msgs[0].OfTool.ToolCallID)
	}
}

func TestConvertMessagesFullConversation(t *testing.T) {
	ctx := ai.Context{
		System: "helper",
		Messages: []ai.Message{
			ai.UserMessage{Content: "hello"},
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", Content: []ai.ContentBlock{ai.TextContent{Text: "data"}}},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}},
		},
	}
	msgs := convertMessages(ctx)
	// system + user + assistant + tool + assistant = 5
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
}

func TestUserMessageString(t *testing.T) {
	msg := userMessage("hello")
	if msg.OfUser == nil {
		t.Fatal("expected OfUser")
	}
}

func TestUserMessageContentBlocks(t *testing.T) {
	blocks := []ai.ContentBlock{ai.TextContent{Text: "test"}}
	msg := userMessage(blocks)
	if msg.OfUser == nil {
		t.Fatal("expected OfUser")
	}
}

func TestUserMessageWithImage(t *testing.T) {
	blocks := []ai.ContentBlock{
		ai.TextContent{Text: "look"},
		ai.ImageContent{Data: "base64", MimeType: "image/png"},
	}
	msg := userMessage(blocks)
	if msg.OfUser == nil {
		t.Fatal("expected OfUser")
	}
}

func TestUserMessageDefault(t *testing.T) {
	msg := userMessage(123)
	if msg.OfUser == nil {
		t.Fatal("expected OfUser for default type")
	}
}

func TestConvertAssistantMessageToolCallOnly(t *testing.T) {
	m := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"x": 1}},
		},
	}
	msg := convertAssistantMessage(m)
	if msg.OfAssistant == nil {
		t.Fatal("expected OfAssistant")
	}
	if len(msg.OfAssistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.OfAssistant.ToolCalls))
	}
}

func TestConvertAssistantMessageEmpty(t *testing.T) {
	m := ai.AssistantMessage{Content: []ai.ContentBlock{}}
	msg := convertAssistantMessage(m)
	if msg.OfAssistant == nil {
		t.Fatal("expected OfAssistant for empty content")
	}
}
