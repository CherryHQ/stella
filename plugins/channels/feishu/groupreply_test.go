package feishu

import (
	"context"
	"testing"
)

func TestGroupReplyToolDefinition(t *testing.T) {
	tool := &GroupReplyTool{}
	def := tool.Definition()

	if def.Name != "group_reply" {
		t.Fatalf("expected name %q, got %q", "group_reply", def.Name)
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in InputSchema")
	}
	if _, ok := props["text"]; !ok {
		t.Fatal("expected 'text' parameter in properties")
	}
}

func TestGroupReplyToolExecuteWithCallback(t *testing.T) {
	tool := &GroupReplyTool{}

	var got string
	ctx := WithGroupReplyFn(context.Background(), func(text string) {
		got = text
	})

	result, err := tool.Execute(ctx, map[string]any{"text": "hello group"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Message sent to group." {
		t.Fatalf("unexpected result: %q", result)
	}
	if got != "hello group" {
		t.Fatalf("callback received %q, want %q", got, "hello group")
	}
}

func TestGroupReplyToolExecuteWithoutCallback(t *testing.T) {
	tool := &GroupReplyTool{}

	_, err := tool.Execute(context.Background(), map[string]any{"text": "hello"})
	if err == nil {
		t.Fatal("expected error when no callback set")
	}
}

func TestGroupReplyToolExecuteEmptyText(t *testing.T) {
	tool := &GroupReplyTool{}

	ctx := WithGroupReplyFn(context.Background(), func(string) {})

	_, err := tool.Execute(ctx, map[string]any{"text": ""})
	if err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestGroupReplyFnFromCtxNil(t *testing.T) {
	fn := GroupReplyFnFromCtx(context.Background())
	if fn != nil {
		t.Fatal("expected nil from plain context")
	}
}
