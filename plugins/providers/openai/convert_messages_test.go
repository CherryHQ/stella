package openai

import (
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
	msgs := convertMessages(ctx)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (tool + user), got %d", len(msgs))
	}
	if msgs[0].OfTool == nil || msgs[0].OfTool.ToolCallID != "call_1" {
		t.Fatalf("first message must be the tool result for call_1")
	}
	if msgs[1].OfUser == nil {
		t.Fatal("second message must be a user message carrying the image")
	}
	if len(msgs[1].OfUser.Content.OfArrayOfContentParts) == 0 {
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
	msgs := convertMessages(ctx)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if got := msgs[0].OfTool.Content.OfString.Value; got == "" {
		t.Error("empty image tool result must get a placeholder string")
	}
}

// An image result from an earlier tool call in a multi-call turn must not insert
// its user image carrier between the tool messages — every tool_call has to be
// answered by a tool message before any other role appears.
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
	msgs := convertMessages(ctx)
	// assistant + tool(c1) + tool(c2) + user(image) = 4
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[1].OfTool == nil || msgs[1].OfTool.ToolCallID != "c1" {
		t.Fatalf("msg[1] must be tool result c1")
	}
	if msgs[2].OfTool == nil || msgs[2].OfTool.ToolCallID != "c2" {
		t.Fatalf("msg[2] must be tool result c2, not the image carrier")
	}
	if msgs[3].OfUser == nil {
		t.Fatalf("msg[3] must be the user image carrier, after all tool results")
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
	msgs := convertMessages(ctx)
	carrier := msgs[len(msgs)-1]
	if carrier.OfUser == nil {
		t.Fatal("last message must be the user image carrier")
	}
	var labels []string
	var images int
	for _, p := range carrier.OfUser.Content.OfArrayOfContentParts {
		if p.OfText != nil {
			labels = append(labels, p.OfText.Text)
		}
		if p.OfImageURL != nil {
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
