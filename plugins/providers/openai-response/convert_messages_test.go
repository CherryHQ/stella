package openairesponse

import (
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestConvertMessagesEmpty(t *testing.T) {
	ctx := ai.Context{}
	items := convertMessages(ctx)
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestConvertMessagesUserText(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.UserMessage{Content: "hello"},
		},
	}
	items := convertMessages(ctx)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].OfMessage == nil {
		t.Fatal("expected OfMessage to be set")
	}
	if items[0].OfMessage.Role != "user" {
		t.Errorf("role = %q, want %q", items[0].OfMessage.Role, "user")
	}
}

func TestConvertMessagesAssistantTextOnly(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "hi there"},
				},
			},
		},
	}
	items := convertMessages(ctx)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].OfMessage == nil {
		t.Fatal("expected OfMessage")
	}
	if items[0].OfMessage.Role != "assistant" {
		t.Errorf("role = %q, want %q", items[0].OfMessage.Role, "assistant")
	}
}

func TestConvertMessagesAssistantToolCall(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "let me check"},
					ai.ToolCall{ID: "call_1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
				},
			},
		},
	}
	items := convertMessages(ctx)
	// Text message + function call = 2 items
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// First should be the text message (prepended)
	if items[0].OfMessage == nil {
		t.Fatal("expected first item to be OfMessage (text)")
	}
	// Second should be the function call
	if items[1].OfFunctionCall == nil {
		t.Fatal("expected second item to be OfFunctionCall")
	}
	if items[1].OfFunctionCall.Name != "bash" {
		t.Errorf("name = %q, want %q", items[1].OfFunctionCall.Name, "bash")
	}
	if items[1].OfFunctionCall.CallID != "call_1" {
		t.Errorf("call_id = %q, want %q", items[1].OfFunctionCall.CallID, "call_1")
	}
}

func TestConvertMessagesToolResult(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.ToolResultMessage{
				ToolCallID: "call_1",
				Content:    []ai.ContentBlock{ai.TextContent{Text: "file.txt"}},
			},
		},
	}
	items := convertMessages(ctx)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].OfFunctionCallOutput == nil {
		t.Fatal("expected OfFunctionCallOutput")
	}
	if items[0].OfFunctionCallOutput.CallID != "call_1" {
		t.Errorf("call_id = %q, want %q", items[0].OfFunctionCallOutput.CallID, "call_1")
	}
	if items[0].OfFunctionCallOutput.Output != "file.txt" {
		t.Errorf("output = %q, want %q", items[0].OfFunctionCallOutput.Output, "file.txt")
	}
}

func TestConvertMessagesToolResultWithImage(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.ToolResultMessage{
				ToolCallID: "call_1",
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "Read image file [image/png]"},
					ai.ImageContent{Data: "base64", MimeType: "image/png"},
				},
			},
		},
	}
	items := convertMessages(ctx)
	if len(items) != 2 {
		t.Fatalf("expected 2 items (output + user), got %d", len(items))
	}
	if items[0].OfFunctionCallOutput == nil || items[0].OfFunctionCallOutput.CallID != "call_1" {
		t.Fatal("first item must be the function call output for call_1")
	}
	if items[1].OfMessage == nil || items[1].OfMessage.Role != "user" {
		t.Fatal("second item must be a user message carrying the image")
	}
	if len(items[1].OfMessage.Content.OfInputItemContentList) == 0 {
		t.Fatal("user message must contain image content parts")
	}
}

func TestConvertMessagesToolResultImageNoText(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.ToolResultMessage{
				ToolCallID: "call_1",
				Content:    []ai.ContentBlock{ai.ImageContent{Data: "base64", MimeType: "image/png"}},
			},
		},
	}
	items := convertMessages(ctx)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].OfFunctionCallOutput.Output == "" {
		t.Error("empty image tool result must get a placeholder output")
	}
}

// An image result from an earlier tool call in a multi-call turn must not insert
// its user image carrier between the function_call_output items.
func TestConvertMessagesMultiToolCallImageOrdering(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}},
				ai.ToolCall{ID: "c2", Name: "bash", Arguments: map[string]any{}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", Content: []ai.ContentBlock{
				ai.TextContent{Text: "Read image file [image/png]"},
				ai.ImageContent{Data: "base64", MimeType: "image/png"},
			}},
			ai.ToolResultMessage{ToolCallID: "c2", Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}},
		},
	}
	items := convertMessages(ctx)
	// assistant(text omitted, only tool calls) -> funcCall c1, c2 then outputs.
	// We only assert the tail ordering of the two outputs + image carrier.
	var outIdx []int
	var userIdx []int
	for i, it := range items {
		if it.OfFunctionCallOutput != nil {
			outIdx = append(outIdx, i)
		}
		if it.OfMessage != nil && it.OfMessage.Role == "user" {
			userIdx = append(userIdx, i)
		}
	}
	if len(outIdx) != 2 {
		t.Fatalf("expected 2 function_call_output items, got %d", len(outIdx))
	}
	if len(userIdx) != 1 {
		t.Fatalf("expected 1 user image carrier, got %d", len(userIdx))
	}
	if userIdx[0] < outIdx[1] {
		t.Fatalf("image carrier (idx %d) must come after both outputs (last at %d)", userIdx[0], outIdx[1])
	}
}

// When several tool results in one turn return images, the carrier user message
// must label each result's images with its tool name and call ID so the model
// can attribute them instead of guessing by position.
func TestConvertMessagesMultiImageAttribution(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}},
				ai.ToolCall{ID: "c2", Name: "read", Arguments: map[string]any{}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ai.ContentBlock{
				ai.ImageContent{Data: "a", MimeType: "image/png"},
			}},
			ai.ToolResultMessage{ToolCallID: "c2", ToolName: "read", Content: []ai.ContentBlock{
				ai.ImageContent{Data: "b", MimeType: "image/png"},
			}},
		},
	}
	items := convertMessages(ctx)
	var carrier responses.ResponseInputMessageContentListParam
	for i := range items {
		if items[i].OfMessage != nil && items[i].OfMessage.Role == "user" {
			carrier = items[i].OfMessage.Content.OfInputItemContentList
		}
	}
	if carrier == nil {
		t.Fatal("expected a user image carrier")
	}
	var labels []string
	var images int
	for _, p := range carrier {
		if p.OfInputText != nil {
			labels = append(labels, p.OfInputText.Text)
		}
		if p.OfInputImage != nil {
			images++
		}
	}
	if images != 2 {
		t.Fatalf("expected 2 images in carrier, got %d", images)
	}
	joined := strings.Join(labels, "\n")
	if !strings.Contains(joined, "c1") || !strings.Contains(joined, "c2") {
		t.Errorf("carrier must label both call IDs, got labels: %q", joined)
	}
}

func TestConvertMessagesMultipleTypes(t *testing.T) {
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.UserMessage{Content: "do something"},
			ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "/tmp"}},
			}},
			ai.ToolResultMessage{ToolCallID: "c1", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}},
		},
	}
	items := convertMessages(ctx)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
}

func TestUserMessageString(t *testing.T) {
	item := userMessage("hello")
	if item.OfMessage == nil {
		t.Fatal("expected OfMessage")
	}
	if item.OfMessage.Role != "user" {
		t.Errorf("role = %q, want user", item.OfMessage.Role)
	}
}

func TestUserMessageContentBlocks(t *testing.T) {
	blocks := []ai.ContentBlock{
		ai.TextContent{Text: "check this"},
	}
	item := userMessage(blocks)
	if item.OfMessage == nil {
		t.Fatal("expected OfMessage")
	}
}

func TestUserMessageWithImage(t *testing.T) {
	blocks := []ai.ContentBlock{
		ai.TextContent{Text: "what is this?"},
		ai.ImageContent{Data: "base64data", MimeType: "image/png"},
	}
	item := userMessage(blocks)
	if item.OfMessage == nil {
		t.Fatal("expected OfMessage")
	}
}

func TestUserMessageDefault(t *testing.T) {
	item := userMessage(42)
	if item.OfMessage == nil {
		t.Fatal("expected OfMessage for default type")
	}
}

func TestConvertAssistantMessageToolCallOnly(t *testing.T) {
	m := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"cmd": "ls"}},
		},
	}
	items := convertAssistantMessage(m)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].OfFunctionCall == nil {
		t.Fatal("expected OfFunctionCall")
	}
}

func TestConvertAssistantMessageEmpty(t *testing.T) {
	m := ai.AssistantMessage{Content: []ai.ContentBlock{}}
	items := convertAssistantMessage(m)
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}
