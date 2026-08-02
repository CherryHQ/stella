package anthropic

import (
	"encoding/json"
	"strings"
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

func TestConvertMessagesMergesConsecutiveUserMessages(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.UserMessage{Content: "first"},
			ai.UserMessage{Content: "second"},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged user message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want user", msgs[0].Role)
	}
	if len(msgs[0].Content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(msgs[0].Content))
	}
}

func TestConvertMessagesMergesToolResultWithUserMessage(t *testing.T) {
	// assistant(tool_call) → tool_result → user(text) should produce:
	// assistant, user (tool_result + text merged)
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", Content: []ai.ContentBlock{ai.TextContent{Text: "output"}}},
			ai.UserMessage{Content: "continue"},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (assistant + merged user), got %d", len(msgs))
	}
	if msgs[1].Role != "user" {
		t.Errorf("second message role = %q, want user", msgs[1].Role)
	}
	// tool_result block + text block = 2 blocks
	if len(msgs[1].Content) != 2 {
		t.Errorf("expected 2 content blocks in merged user message, got %d", len(msgs[1].Content))
	}
}

func TestConvertMessagesMergesMultipleToolResults(t *testing.T) {
	// Parallel tool calls: assistant(2 calls) → result1 → result2
	// Both tool results are user-role and should merge into one user message.
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}},
				ai.ToolCall{ID: "c2", Name: "bash", Arguments: map[string]any{}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", Content: []ai.ContentBlock{ai.TextContent{Text: "file"}}},
			ai.ToolResultMessage{ToolCallID: "c2", Content: []ai.ContentBlock{ai.TextContent{Text: "output"}}},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (assistant + merged tool results), got %d", len(msgs))
	}
	if msgs[1].Role != "user" {
		t.Errorf("second message role = %q, want user", msgs[1].Role)
	}
	if len(msgs[1].Content) != 2 {
		t.Errorf("expected 2 tool_result blocks, got %d", len(msgs[1].Content))
	}
}

func TestConvertMessagesMergesConsecutiveAssistantMessages(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "first"}}},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "second"}}},
		},
	}
	msgs := convertMessages(ctx)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged assistant message, got %d", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(msgs[0].Content))
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

func TestToolResultBlockPreservesAllProjectedText(t *testing.T) {
	block := toolResultBlock(ai.ToolResultMessage{
		ToolCallID: "t1",
		Content: []ai.ContentBlock{
			ai.TextContent{Text: "Read image file [image/png]"},
			ai.TextContent{Text: "## Text\nwords\n\n## Scene\nA screenshot."},
		},
	})
	payload, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Read image file", "## Text", "## Scene"} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("tool result JSON omitted %q: %s", want, payload)
		}
	}
}

func TestAssistantContentBlocksToolCallNilArgs(t *testing.T) {
	// A tool call with nil Arguments must produce an empty-object input, not null,
	// or the Anthropic API returns 400 on the next turn.
	content := []ai.ContentBlock{
		ai.ToolCall{ID: "t1", Name: "bash", Arguments: nil},
	}
	blocks := assistantContentBlocks(content)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	tu := blocks[0].OfToolUse
	if tu == nil {
		t.Fatal("expected OfToolUse block")
	}
	if tu.Input == nil {
		t.Error("input must not be nil (would serialize as JSON null and trigger 400)")
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
