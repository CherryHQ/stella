package runner

import (
	"context"
	"testing"

	"github.com/vaayne/anna/ai"
)

func TestHandlerFunc(t *testing.T) {
	fn := HandlerFunc(func(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event {
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
