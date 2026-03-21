package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestSearchToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSearchTool(client)

	def := tool.Definition()
	if def.Name != "feishu_search" {
		t.Fatalf("expected name feishu_search, got %q", def.Name)
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
		"search_docs": true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestSearchToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSearchTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestBuildSearchFilter(t *testing.T) {
	// With all params.
	args := map[string]any{
		"doc_types":   []any{"DOC", "SHEET"},
		"creator_ids": []any{"ou_test"},
		"only_title":  true,
		"sort_type":   "EDIT_TIME",
	}
	filter := buildSearchFilter(args)
	if filter == nil {
		t.Fatal("expected non-nil filter")
	}
	if len(filter.DocTypes) != 2 {
		t.Fatalf("expected 2 doc types, got %d", len(filter.DocTypes))
	}
	if len(filter.CreatorIds) != 1 {
		t.Fatalf("expected 1 creator id, got %d", len(filter.CreatorIds))
	}
	if filter.OnlyTitle == nil || !*filter.OnlyTitle {
		t.Fatal("expected only_title to be true")
	}
	if filter.SortType == nil || *filter.SortType != "EDIT_TIME" {
		t.Fatal("expected sort_type to be EDIT_TIME")
	}

	// Empty params.
	emptyFilter := buildSearchFilter(map[string]any{})
	if emptyFilter == nil {
		t.Fatal("expected non-nil empty filter")
	}
}

func TestBuildSearchWikiFilter(t *testing.T) {
	args := map[string]any{
		"doc_types": []any{"WIKI"},
	}
	filter := buildSearchWikiFilter(args)
	if filter == nil {
		t.Fatal("expected non-nil wiki filter")
	}
	if len(filter.DocTypes) != 1 {
		t.Fatalf("expected 1 doc type, got %d", len(filter.DocTypes))
	}
}
