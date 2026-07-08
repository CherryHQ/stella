package server

import "testing"

func TestToolInputSchema(t *testing.T) {
	if got := toolInputSchema(nil); got != nil {
		t.Errorf("nil schema should map to nil, got %v", got)
	}
	if got := toolInputSchema(map[string]any{}); got != nil {
		t.Errorf("empty schema should map to nil, got %v", got)
	}
	schema := map[string]any{"type": "object", "required": []any{"action"}}
	got := toolInputSchema(schema)
	if got == nil {
		t.Fatal("non-empty schema should be returned")
	}
	if (*got)["type"] != "object" {
		t.Errorf("schema content not preserved: %v", *got)
	}
}
