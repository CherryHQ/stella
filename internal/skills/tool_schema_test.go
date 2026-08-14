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

func TestRuntimeToolExposesOnlyReadActions(t *testing.T) {
	tool := newProjectionTool(t, &projectionReader{}, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
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
		t.Fatal("runtime skills tool must reject removed write actions")
	}
}

func TestNewToolRequiresOneExactRuntimeContract(t *testing.T) {
	runtime := &projectionReader{}
	session := projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}
	authorizer := allowAllSkillReads{}
	for name, build := range map[string]func() (*Tool, error){
		"runtime":    func() (*Tool, error) { return NewTool(nil, session, authorizer) },
		"Session":    func() (*Tool, error) { return NewTool(runtime, nil, authorizer) },
		"authorizer": func() (*Tool, error) { return NewTool(runtime, session, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			if tool, err := build(); err == nil || tool != nil {
				t.Fatalf("NewTool without %s = %#v, %v", name, tool, err)
			}
		})
	}
}
