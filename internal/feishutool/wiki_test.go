package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestWikiToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	def := tool.Definition()
	if def.Name != "feishu_wiki" {
		t.Fatalf("expected name feishu_wiki, got %q", def.Name)
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
		"list_spaces":       true,
		"get_space":         true,
		"create_space_node": true,
		"list_space_nodes":  true,
		"get_node":          true,
		"move_node":         true,
		"copy_node":         true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestWikiToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestWikiToolGetSpaceMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_space",
	})
	if err == nil {
		t.Fatal("expected error for missing space_id")
	}
}

func TestWikiToolCreateNodeMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":   "create_space_node",
		"space_id": "space123",
	})
	if err == nil {
		t.Fatal("expected error for missing obj_type and node_type")
	}
}

func TestWikiToolListNodesMissingSpace(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "list_space_nodes",
	})
	if err == nil {
		t.Fatal("expected error for missing space_id")
	}
}

func TestWikiToolGetNodeMissingToken(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_node",
	})
	if err == nil {
		t.Fatal("expected error for missing node_token")
	}
}

func TestWikiToolMoveNodeMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "move_node",
	})
	if err == nil {
		t.Fatal("expected error for missing space_id and node_token")
	}
}

func TestWikiToolCopyNodeMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewWikiTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":   "copy_node",
		"space_id": "space123",
	})
	if err == nil {
		t.Fatal("expected error for missing node_token")
	}
}
