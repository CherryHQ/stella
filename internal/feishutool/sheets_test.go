package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestSheetsToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSheetsTool(client)

	def := tool.Definition()
	if def.Name != "feishu_sheets" {
		t.Fatalf("expected name feishu_sheets, got %q", def.Name)
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
		"create_spreadsheet": true,
		"get_spreadsheet":    true,
		"list_sheets":        true,
		"read_range":         true,
		"write_range":        true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestSheetsToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSheetsTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestSheetsToolGetSpreadsheetMissingToken(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSheetsTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_spreadsheet",
	})
	if err == nil {
		t.Fatal("expected error for missing spreadsheet_token")
	}
}

func TestSheetsToolListSheetsMissingToken(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSheetsTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "list_sheets",
	})
	if err == nil {
		t.Fatal("expected error for missing spreadsheet_token")
	}
}

func TestSheetsToolReadRangeMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSheetsTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "read_range",
	})
	if err == nil {
		t.Fatal("expected error for missing spreadsheet_token and range")
	}

	_, err = tool.Execute(context.Background(), map[string]any{
		"action":            "read_range",
		"spreadsheet_token": "test_token",
	})
	if err == nil {
		t.Fatal("expected error for missing range")
	}
}

func TestSheetsToolWriteRangeMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewSheetsTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":            "write_range",
		"spreadsheet_token": "test_token",
		"range":             "sheet1!A1:B2",
	})
	if err == nil {
		t.Fatal("expected error for missing values")
	}
}
