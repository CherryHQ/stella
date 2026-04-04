package selfimprove

import (
	"context"
	"testing"
)

func TestReviewMemoryToolDefinition(t *testing.T) {
	t.Parallel()

	tool := NewReviewMemoryTool(nil, 1, "agent-1")
	def := tool.Definition()

	if def.Name != "review_memory" {
		t.Errorf("name = %q, want %q", def.Name, "review_memory")
	}
	if def.InputSchema == nil {
		t.Error("input schema is nil")
	}
}

func TestReviewMemoryToolUnknownAction(t *testing.T) {
	t.Parallel()

	tool := NewReviewMemoryTool(nil, 1, "agent-1")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "delete"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestReviewMemoryToolUpdateRequiresContent(t *testing.T) {
	t.Parallel()

	tool := NewReviewMemoryTool(nil, 1, "agent-1")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "update"})
	if err == nil {
		t.Fatal("expected error when content is empty")
	}
}

func TestReviewMemoryToolUpdateEmptyContent(t *testing.T) {
	t.Parallel()

	tool := NewReviewMemoryTool(nil, 1, "agent-1")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "update", "content": ""})
	if err == nil {
		t.Fatal("expected error when content is empty string")
	}
}
