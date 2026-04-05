package anthropic

import (
	"testing"

	"github.com/vaayne/anna/pkg/ai"
)

func TestConvertMessagesEmpty(t *testing.T) {
	ctx := ai.Context{}
	msgs := convertMessages(ctx)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
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
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want user", msgs[0].Role)
	}
}

func TestConvertMessagesAssistantText(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: "response"},
			}},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("role = %q, want assistant", msgs[0].Role)
	}
}

func TestConvertMessagesAssistantWithToolCall(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: "let me check"},
				ai.ToolCall{ID: "t1", Name: "bash", Arguments: map[string]any{"cmd": "ls"}},
			}},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msgs[0].Content))
	}
}

func TestConvertMessagesAssistantWithThinking(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ThinkingContent{Thinking: "reasoning here", Signature: "sig1"},
				ai.TextContent{Text: "answer"},
			}},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msgs[0].Content))
	}
}

func TestConvertMessagesToolResult(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.ToolResultMessage{
				ToolCallID: "t1",
				Content:    []ai.ContentBlock{ai.TextContent{Text: "output"}},
			},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want user (tool results wrapped in user)", msgs[0].Role)
	}
}

func TestConvertMessagesToolResultError(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.ToolResultMessage{
				ToolCallID: "t1",
				Content:    []ai.ContentBlock{ai.TextContent{Text: "failed"}},
				IsError:    true,
			},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

func TestConvertMessagesFullConversation(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.UserMessage{Content: "hi"},
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", Content: []ai.ContentBlock{ai.TextContent{Text: "data"}}},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
}

func TestUserContentBlocksString(t *testing.T) {
	blocks := userContentBlocks("hello")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestUserContentBlocksMultipleBlocks(t *testing.T) {
	content := []ai.ContentBlock{
		ai.TextContent{Text: "text1"},
		ai.TextContent{Text: "text2"},
	}
	blocks := userContentBlocks(content)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestUserContentBlocksWithImage(t *testing.T) {
	content := []ai.ContentBlock{
		ai.TextContent{Text: "look"},
		ai.ImageContent{Data: "base64data", MimeType: "image/jpeg"},
	}
	blocks := userContentBlocks(content)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestUserContentBlocksDefault(t *testing.T) {
	blocks := userContentBlocks(42)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestAssistantContentBlocksEmpty(t *testing.T) {
	blocks := assistantContentBlocks(nil)
	if len(blocks) != 0 {
		t.Fatalf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestAssistantContentBlocksMixed(t *testing.T) {
	content := []ai.ContentBlock{
		ai.TextContent{Text: "text"},
		ai.ThinkingContent{Thinking: "hmm", Signature: "sig"},
		ai.ToolCall{ID: "t1", Name: "bash", Arguments: map[string]any{}},
	}
	blocks := assistantContentBlocks(content)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
}

func TestToolResultBlockBasic(t *testing.T) {
	m := ai.ToolResultMessage{
		ToolCallID: "t1",
		Content:    []ai.ContentBlock{ai.TextContent{Text: "result"}},
	}
	block := toolResultBlock(m)
	if block.OfToolResult == nil {
		t.Fatal("expected OfToolResult")
	}
	if block.OfToolResult.ToolUseID != "t1" {
		t.Errorf("tool_use_id = %q, want t1", block.OfToolResult.ToolUseID)
	}
}

func TestToolResultBlockEmpty(t *testing.T) {
	m := ai.ToolResultMessage{
		ToolCallID: "t1",
		Content:    []ai.ContentBlock{},
	}
	block := toolResultBlock(m)
	if block.OfToolResult == nil {
		t.Fatal("expected OfToolResult")
	}
}
