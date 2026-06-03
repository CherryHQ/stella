package skills

import (
	"sort"
	"testing"
)

func TestSplitToolsNamesAndSchemas(t *testing.T) {
	ts := NewSplitTools(nil, "", "", "", "")
	if ts != nil {
		t.Fatalf("NewSplitTools(nil store) = %v, want nil", ts)
	}

	defs := []map[string]any{searchDef().InputSchema, listDef().InputSchema, installDef().InputSchema}
	names := []string{searchDef().Name, listDef().Name, installDef().Name}
	sort.Strings(names)
	want := []string{"skill_install", "skill_list", "skill_search"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names = %v, want %v", names, want)
			break
		}
	}
	for i, schema := range defs {
		props, _ := schema["properties"].(map[string]any)
		for _, banned := range []string{"agent_id", "user_id", "session_id"} {
			if _, ok := props[banned]; ok {
				t.Errorf("def %d exposes identity prop %q", i, banned)
			}
		}
	}
}
