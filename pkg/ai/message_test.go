package ai

import (
	"fmt"
	"testing"
	"time"
)

func TestContentBlockKinds(t *testing.T) {
	tests := []struct {
		name  string
		block ContentBlock
		want  string
	}{
		{"text", TextContent{Text: "hi"}, "text"},
		{"thinking", ThinkingContent{Thinking: "hmm"}, "thinking"},
		{"image", ImageContent{Data: "x", MimeType: "image/png"}, "image"},
		{"toolCall", ToolCall{ID: "1", Name: "test"}, "toolCall"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.block.contentBlockKind(); got != tt.want {
				t.Errorf("contentBlockKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImageContentDataURI(t *testing.T) {
	ic := ImageContent{Data: "abc123", MimeType: "image/jpeg"}
	want := "data:image/jpeg;base64,abc123"
	if got := ic.DataURI(); got != want {
		t.Errorf("DataURI() = %q, want %q", got, want)
	}
}

func TestMessageRoles(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{"user", UserMessage{}, "user"},
		{"assistant", AssistantMessage{}, "assistant"},
		{"tool", ToolResultMessage{}, "tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.messageRole(); got != tt.want {
				t.Errorf("messageRole() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasImage(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ContentBlock
		want   bool
	}{
		{"nil", nil, false},
		{"empty", []ContentBlock{}, false},
		{"text only", []ContentBlock{TextContent{Text: "hi"}}, false},
		{"image only", []ContentBlock{ImageContent{Data: "x", MimeType: "image/png"}}, true},
		{"mixed", []ContentBlock{TextContent{Text: "hi"}, ImageContent{Data: "x", MimeType: "image/png"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasImage(tt.blocks); got != tt.want {
				t.Errorf("HasImage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFlattenText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ContentBlock
		want   string
	}{
		{"nil", nil, ""},
		{"empty", []ContentBlock{}, ""},
		{"single text", []ContentBlock{TextContent{Text: "hello"}}, "hello"},
		{"multiple text", []ContentBlock{TextContent{Text: "a"}, TextContent{Text: "b"}}, "a b"},
		{"mixed with image", []ContentBlock{
			TextContent{Text: "describe"},
			ImageContent{Data: "x", MimeType: "image/png"},
			TextContent{Text: "this"},
		}, "describe this"},
		{"empty text skipped", []ContentBlock{TextContent{Text: ""}, TextContent{Text: "only"}}, "only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FlattenText(tt.blocks); got != tt.want {
				t.Errorf("FlattenText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTimestampedContent(t *testing.T) {
	ts := time.Unix(1745920200, 0)
	prefix := fmt.Sprintf("ts:%d\n", ts.Unix())

	t.Run("zero timestamp returns original", func(t *testing.T) {
		m := UserMessage{Content: "hello"}
		got := m.TimestampedContent()
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("string content", func(t *testing.T) {
		m := UserMessage{Content: "hello", Timestamp: ts}
		got := m.TimestampedContent().(string)
		want := prefix + "hello"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("content blocks with leading text", func(t *testing.T) {
		m := UserMessage{
			Content:   []ContentBlock{TextContent{Text: "hello"}},
			Timestamp: ts,
		}
		blocks := m.TimestampedContent().([]ContentBlock)
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		got := blocks[0].(TextContent).Text
		want := prefix + "hello"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("content blocks with leading image", func(t *testing.T) {
		m := UserMessage{
			Content:   []ContentBlock{ImageContent{Data: "x", MimeType: "image/png"}},
			Timestamp: ts,
		}
		blocks := m.TimestampedContent().([]ContentBlock)
		if len(blocks) != 2 {
			t.Fatalf("got %d blocks, want 2", len(blocks))
		}
		got := blocks[0].(TextContent).Text
		want := fmt.Sprintf("ts:%d", ts.Unix())
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
