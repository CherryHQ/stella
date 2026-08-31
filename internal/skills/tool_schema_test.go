package skills

import (
	"context"
	"slices"
	"testing"
)

// The runtime tool reads; it never writes. After the split that is two names
// with two sealed schemas, so "which actions exist" is the name list itself.
func TestRuntimeToolsExposeOnlyReadActionsWithSealedSchemas(t *testing.T) {
	names := make([]string, 0, len(RuntimeActionTools()))
	for _, spec := range RuntimeActionTools() {
		names = append(names, spec.Name)
	}
	if !slices.Equal(names, []string{"skill_load", "skill_installed_search"}) {
		t.Fatalf("skill tools = %v, want the two read actions", names)
	}
	for _, spec := range RuntimeActionTools() {
		schema := spec.InputSchema()
		if sealed, _ := schema["additionalProperties"].(bool); sealed {
			t.Errorf("%s accepts extra properties", spec.Name)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no properties: %#v", spec.Name, schema["properties"])
		}
		// knowledge_type is the retired knowledge classification; the rest are
		// the management fields the runtime tool never had.
		for _, hidden := range []string{"action", "knowledge_type", "source", "scope", "description", "content", "status"} {
			if _, ok := properties[hidden]; ok {
				t.Errorf("read-only %s schema must omit %q", spec.Name, hidden)
			}
		}
	}

	tool := newProjectionTool(t, &projectionReader{}, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
	if _, err := SkillDispatch(context.Background(), tool, "patch", map[string]any{"name": "owned"}); err == nil {
		t.Fatal("runtime skill tools must reject removed write actions")
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
