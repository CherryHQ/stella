package runner

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vaayne/anna/ai"
)

func TestHandlerFunc(t *testing.T) {
	fn := HandlerFunc(func(ctx context.Context, history []RPCEvent, message MessageContent) <-chan Event {
		ch := make(chan Event, 1)
		ch <- Event{Text: "hello from handler: " + MessageText(message)}
		close(ch)
		return ch
	})

	// Verify it satisfies the Runner interface.
	var r Runner = fn
	stream := r.Chat(context.Background(), nil, "test")

	evt := <-stream
	if evt.Err != nil {
		t.Fatalf("unexpected error: %v", evt.Err)
	}
	if evt.Text != "hello from handler: test" {
		t.Errorf("text = %q, want %q", evt.Text, "hello from handler: test")
	}
}

func TestMessageTextString(t *testing.T) {
	got := MessageText("hello world")
	if got != "hello world" {
		t.Errorf("MessageText(string) = %q, want %q", got, "hello world")
	}
}

func TestMessageTextMultimodal(t *testing.T) {
	blocks := []ai.ContentBlock{
		ai.TextContent{Text: "describe"},
		ai.ImageContent{Data: "abc", MimeType: "image/png"},
		ai.TextContent{Text: "this image"},
	}
	got := MessageText(blocks)
	want := "describe this image"
	if got != want {
		t.Errorf("MessageText(multimodal) = %q, want %q", got, want)
	}
}

func TestMessageTextEmpty(t *testing.T) {
	blocks := []ai.ContentBlock{
		ai.ImageContent{Data: "abc", MimeType: "image/png"},
	}
	got := MessageText(blocks)
	if got != "" {
		t.Errorf("MessageText(image-only) = %q, want empty", got)
	}
}

func TestUserMessageToRPCEventString(t *testing.T) {
	evt := UserMessageToRPCEvent("hello")
	if evt.Type != RPCEventUserMessage {
		t.Errorf("Type = %q, want %q", evt.Type, RPCEventUserMessage)
	}
	if evt.Summary != "hello" {
		t.Errorf("Summary = %q, want %q", evt.Summary, "hello")
	}
	if evt.Content != nil {
		t.Errorf("Content should be nil for text-only, got %s", string(evt.Content))
	}
}

func TestUserMessageToRPCEventMultimodal(t *testing.T) {
	blocks := []ai.ContentBlock{
		ai.TextContent{Text: "caption"},
		ai.ImageContent{Data: "base64data", MimeType: "image/jpeg"},
	}
	evt := UserMessageToRPCEvent(blocks)

	if evt.Type != RPCEventUserMessage {
		t.Errorf("Type = %q, want %q", evt.Type, RPCEventUserMessage)
	}
	if evt.Summary != "caption" {
		t.Errorf("Summary = %q, want %q", evt.Summary, "caption")
	}
	if evt.Content == nil {
		t.Fatal("Content should not be nil for multimodal")
	}

	var stored []ContentBlockJSON
	if err := json.Unmarshal(evt.Content, &stored); err != nil {
		t.Fatalf("unmarshal Content: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("len(stored) = %d, want 2", len(stored))
	}
	if stored[0].Kind != "text" || stored[0].Text != "caption" {
		t.Errorf("stored[0] = %+v, want text/caption", stored[0])
	}
	if stored[1].Kind != "image" || stored[1].Data != "base64data" {
		t.Errorf("stored[1] = %+v, want image/base64data", stored[1])
	}
}

func TestDecodeUserContentRoundTrip(t *testing.T) {
	original := []ai.ContentBlock{
		ai.TextContent{Text: "hello"},
		ai.ImageContent{Data: "imgdata", MimeType: "image/png"},
	}

	// Encode
	evt := UserMessageToRPCEvent(original)

	// Decode
	decoded := decodeUserContent(evt)
	blocks, ok := decoded.([]ai.ContentBlock)
	if !ok {
		t.Fatalf("decodeUserContent returned %T, want []ContentBlock", decoded)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}

	text, ok := blocks[0].(ai.TextContent)
	if !ok || text.Text != "hello" {
		t.Errorf("blocks[0] = %+v, want TextContent{Text: hello}", blocks[0])
	}
	img, ok := blocks[1].(ai.ImageContent)
	if !ok || img.Data != "imgdata" || img.MimeType != "image/png" {
		t.Errorf("blocks[1] = %+v, want ImageContent{imgdata, image/png}", blocks[1])
	}
}

func TestDecodeUserContentTextOnly(t *testing.T) {
	evt := UserMessageToRPCEvent("just text")
	decoded := decodeUserContent(evt)
	s, ok := decoded.(string)
	if !ok {
		t.Fatalf("decodeUserContent returned %T, want string", decoded)
	}
	if s != "just text" {
		t.Errorf("decoded = %q, want %q", s, "just text")
	}
}

func TestTextDeltaToRPCEvent(t *testing.T) {
	evt := TextDeltaToRPCEvent("hello")
	if evt.Type != "message_update" {
		t.Errorf("type = %q, want message_update", evt.Type)
	}
	if len(evt.AssistantMessageEvent) == 0 {
		t.Error("expected non-empty AssistantMessageEvent")
	}
}

func TestAssistantMessageToRPCEvent(t *testing.T) {
	evt := AssistantMessageToRPCEvent("test response")
	if evt.Type != RPCEventMessageUpdate {
		t.Errorf("Type = %q, want %q", evt.Type, RPCEventMessageUpdate)
	}
	if evt.Summary != "test response" {
		t.Errorf("Summary = %q, want %q", evt.Summary, "test response")
	}
}

func TestToolCallToRPCEvent(t *testing.T) {
	call := ai.ToolCall{
		ID:        "call-1",
		Name:      "read_file",
		Arguments: map[string]any{"path": "/tmp/test.txt"},
	}
	evt := ToolCallToRPCEvent(call)
	if evt.Type != RPCEventToolCall {
		t.Errorf("Type = %q, want %q", evt.Type, RPCEventToolCall)
	}
	if evt.ID != "call-1" {
		t.Errorf("ID = %q, want %q", evt.ID, "call-1")
	}
	if evt.Tool != "read_file" {
		t.Errorf("Tool = %q, want %q", evt.Tool, "read_file")
	}
}

func TestToolResultToRPCEvent(t *testing.T) {
	result := ai.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Content:    []ai.ContentBlock{ai.TextContent{Text: "file contents"}},
		IsError:    false,
	}
	evt := ToolResultToRPCEvent(result)
	if evt.Type != RPCEventToolResult {
		t.Errorf("Type = %q, want %q", evt.Type, RPCEventToolResult)
	}
	if evt.ID != "call-1" {
		t.Errorf("ID = %q, want %q", evt.ID, "call-1")
	}
	if evt.Error != "" {
		t.Errorf("Error should be empty, got %q", evt.Error)
	}
}

func TestToolResultToRPCEventError(t *testing.T) {
	result := ai.ToolResultMessage{
		ToolCallID: "call-2",
		ToolName:   "read_file",
		Content:    []ai.ContentBlock{ai.TextContent{Text: "not found"}},
		IsError:    true,
	}
	evt := ToolResultToRPCEvent(result)
	if evt.Error != "not found" {
		t.Errorf("Error = %q, want %q", evt.Error, "not found")
	}
}
