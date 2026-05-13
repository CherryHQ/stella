package agent

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

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
