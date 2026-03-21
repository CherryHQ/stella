package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestDocToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDocTool(client)

	def := tool.Definition()
	if def.Name != "feishu_doc" {
		t.Fatalf("expected name feishu_doc, got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if def.InputSchema == nil {
		t.Fatal("expected non-nil input schema")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in input schema")
	}
	actionProp, _ := props["action"].(map[string]any)
	enumVals, _ := actionProp["enum"].([]any)
	expected := map[string]bool{
		"create_doc":          true,
		"get_doc_content":     true,
		"get_doc_raw_content": true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestDocToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDocTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestDocToolGetContentMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDocTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_doc_content",
	})
	if err == nil {
		t.Fatal("expected error for missing document_id")
	}
}

func TestDocToolGetRawContentMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDocTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_doc_raw_content",
	})
	if err == nil {
		t.Fatal("expected error for missing document_id")
	}
}
