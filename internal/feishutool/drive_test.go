package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestDriveToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	def := tool.Definition()
	if def.Name != "feishu_drive" {
		t.Fatalf("expected name feishu_drive, got %q", def.Name)
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
		"list_files":    true,
		"get_file_meta": true,
		"copy_file":     true,
		"move_file":     true,
		"delete_file":   true,
		"create_folder": true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestDriveToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestDriveToolGetFileMetaMissingDocs(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_file_meta",
	})
	if err == nil {
		t.Fatal("expected error for missing request_docs")
	}

	// Empty docs.
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":       "get_file_meta",
		"request_docs": []any{},
	})
	if err == nil {
		t.Fatal("expected error for empty request_docs")
	}

	// Invalid docs (no valid fields).
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":       "get_file_meta",
		"request_docs": []any{map[string]any{"invalid": true}},
	})
	if err == nil {
		t.Fatal("expected error for invalid request_docs")
	}
}

func TestDriveToolCopyFileMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "copy_file",
		"file_token": "test_token",
		"name":       "copy",
	})
	if err == nil {
		t.Fatal("expected error for missing file_type")
	}
}

func TestDriveToolMoveFileMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "move_file",
		"file_token": "test_token",
	})
	if err == nil {
		t.Fatal("expected error for missing file_type and folder_token")
	}
}

func TestDriveToolDeleteFileMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "delete_file",
		"file_token": "test_token",
	})
	if err == nil {
		t.Fatal("expected error for missing file_type")
	}
}

func TestDriveToolCreateFolderMissingName(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewDriveTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "create_folder",
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}
