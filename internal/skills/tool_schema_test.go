package skills

import (
	"context"
	"slices"
	"testing"
)

func TestSkillsSchemaDoesNotExposeKnowledgeType(t *testing.T) {
	props, ok := skillsInputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing or malformed: %#v", skillsInputSchema["properties"])
	}
	if _, ok := props["knowledge_type"]; ok {
		t.Fatal("skills tool must not expose legacy knowledge classification; use facts-backed knowledge instead")
	}
}

func TestSkillsSchemaDoesNotExposeDeprecate(t *testing.T) {
	definition := NewTool(nil, "", "").Definition()
	action := definition.InputSchema["properties"].(map[string]any)["action"].(map[string]any)
	for _, raw := range action["enum"].([]any) {
		if raw == "deprecate" {
			t.Fatal("skills tool must not advertise unsupported deprecate")
		}
	}
	if _, err := NewTool(nil, "", "").Execute(context.Background(), map[string]any{"action": "deprecate"}); err == nil {
		t.Fatal("skills tool accepted unsupported deprecate")
	}
}

func TestToolWithActionsOnlyRestrictsSchemaAndExecution(t *testing.T) {
	tool := NewTool(nil, "", "").WithActionsOnly("search_installed", "load")
	definition := tool.Definition()

	properties, ok := definition.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing or malformed: %#v", definition.InputSchema["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("action schema missing or malformed: %#v", properties["action"])
	}
	rawEnum, ok := action["enum"].([]any)
	if !ok {
		t.Fatalf("action enum missing or malformed: %#v", action["enum"])
	}
	actions := make([]string, 0, len(rawEnum))
	for _, raw := range rawEnum {
		value, ok := raw.(string)
		if !ok {
			t.Fatalf("non-string action enum value: %#v", raw)
		}
		actions = append(actions, value)
	}
	if !slices.Equal(actions, []string{"load", "search_installed"}) {
		t.Fatalf("actions = %v, want read-only runtime actions", actions)
	}
	for _, hidden := range []string{"source", "scope", "description", "content", "status"} {
		if _, ok := properties[hidden]; ok {
			t.Errorf("read-only skills schema must omit %q", hidden)
		}
	}

	if _, err := tool.Execute(context.Background(), map[string]any{"action": "patch", "name": "owned"}); err == nil {
		t.Fatal("restricted skills tool must reject hidden write actions")
	}
}
