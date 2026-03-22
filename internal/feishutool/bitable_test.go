package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestBitableToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	def := tool.Definition()
	if def.Name != "feishu_bitable" {
		t.Fatalf("expected name feishu_bitable, got %q", def.Name)
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
		"create_app":           true,
		"list_tables":          true,
		"create_table":         true,
		"list_records":         true,
		"create_record":        true,
		"update_record":        true,
		"delete_record":        true,
		"batch_create_records": true,
		"batch_update_records": true,
		"batch_delete_records": true,
		"list_fields":          true,
		"create_field":         true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestBitableToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestBitableToolCreateAppMissingName(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "create_app",
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestBitableToolListTablesMissingToken(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "list_tables",
	})
	if err == nil {
		t.Fatal("expected error for missing app_token")
	}
}

func TestBitableToolCreateTableMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "create_table",
		"app_token": "tok_123",
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestBitableToolCreateRecordMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	// Missing fields.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "create_record",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
	})
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
}

func TestBitableToolUpdateRecordMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "update_record",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
	})
	if err == nil {
		t.Fatal("expected error for missing record_id")
	}
}

func TestBitableToolDeleteRecordMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "delete_record",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
	})
	if err == nil {
		t.Fatal("expected error for missing record_id")
	}
}

func TestBitableToolBatchCreateRecordsValidation(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	// Missing records.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "batch_create_records",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
	})
	if err == nil {
		t.Fatal("expected error for missing records")
	}

	// Over batch limit.
	bigRecords := make([]any, 501)
	for i := range bigRecords {
		bigRecords[i] = map[string]any{"fields": map[string]any{"name": "x"}}
	}
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":    "batch_create_records",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
		"records":   bigRecords,
	})
	if err == nil {
		t.Fatal("expected error for exceeding batch limit")
	}
}

func TestBitableToolBatchDeleteRecordsValidation(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "batch_delete_records",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
	})
	if err == nil {
		t.Fatal("expected error for missing record_ids")
	}
}

func TestBitableToolListFieldsMissingToken(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "list_fields",
	})
	if err == nil {
		t.Fatal("expected error for missing app_token/table_id")
	}
}

func TestBitableToolCreateFieldMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewBitableTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "create_field",
		"app_token": "tok_123",
		"table_id":  "tbl_123",
		"name":      "Col1",
	})
	if err == nil {
		t.Fatal("expected error for missing field_type")
	}
}

func TestBuildBitableFilter(t *testing.T) {
	filter := map[string]any{
		"conjunction": "and",
		"conditions": []any{
			map[string]any{
				"field_name": "Status",
				"operator":   "is",
				"value":      []any{"Done"},
			},
			map[string]any{
				"field_name": "Name",
				"operator":   "isEmpty",
				// value intentionally omitted — should auto-fill.
			},
		},
	}

	result := buildBitableFilter(filter)
	if result == nil {
		t.Fatal("expected non-nil filter")
	}
	if result.Conjunction == nil || *result.Conjunction != "and" {
		t.Fatal("expected conjunction 'and'")
	}
	if len(result.Conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(result.Conditions))
	}
	// isEmpty condition should have value=[] (not nil).
	cond := result.Conditions[1]
	if cond.Value == nil {
		t.Fatal("isEmpty condition should have non-nil value")
	}
}

func TestBuildBitableSort(t *testing.T) {
	raw := []any{
		map[string]any{"field_name": "Created", "desc": true},
		map[string]any{"field_name": "Name", "desc": false},
		map[string]any{"bad": "entry"},
	}

	sorts := buildBitableSort(raw)
	if len(sorts) != 2 {
		t.Fatalf("expected 2 sorts, got %d", len(sorts))
	}
}

func TestBuildBitableRecords(t *testing.T) {
	raw := []any{
		map[string]any{"fields": map[string]any{"Name": "A"}},
		map[string]any{"fields": map[string]any{"Name": "B"}},
		map[string]any{}, // no fields, skipped
	}

	records := buildBitableRecords(raw)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

func TestBuildBitableRecordsWithID(t *testing.T) {
	raw := []any{
		map[string]any{"record_id": "rec1", "fields": map[string]any{"Name": "A"}},
		map[string]any{"fields": map[string]any{"Name": "B"}}, // no ID, skipped
	}

	records := buildBitableRecordsWithID(raw)
	if len(records) != 1 {
		t.Fatalf("expected 1 record with ID, got %d", len(records))
	}
}
