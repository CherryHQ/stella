package delegate

import (
	"slices"
	"testing"

	"github.com/CherryHQ/stella/pkg/toolmeta"
)

// A preset's tools: list is a user-written file that predates the split. It
// names "goal" because that was the tool; after the split the same line must
// still grant every goal action and nothing else. The family fixture is
// synthetic because internal/goal imports internal/agent — cmd/stellad covers
// the real generated names.
func TestPresetWhitelistNamingAFamilyKeepsEveryActionInIt(t *testing.T) {
	meta := toolmeta.NewRegistry(
		toolmeta.ActionTool{Name: "goal_create", Family: "goal", Action: "create"},
		toolmeta.ActionTool{Name: "goal_get", Family: "goal", Action: "get"},
		toolmeta.ActionTool{Name: "goal_list", Family: "goal", Action: "list"},
		toolmeta.ActionTool{Name: "goal_update", Family: "goal", Action: "update"},
		toolmeta.ActionTool{Name: "workflow_run", Family: "workflow", Action: "run"},
	)
	reg := registryWith(t, "goal_create", "goal_get", "goal_list", "goal_update", "workflow_run", "goal_helper", "read_file")
	tool := NewDelegateTool(DelegateConfig{SessionRunner: &capturingRunner{}, Registry: reg, ToolMeta: meta})

	excluded := tool.excludedTools([]string{"goal"}, true)
	slices.Sort(excluded)
	want := []string{"goal_helper", "read_file", "workflow_run"}
	if !slices.Equal(excluded, want) {
		t.Fatalf("excluded = %v, want %v", excluded, want)
	}
}

// Without a registry the preset resolver must degrade to exact names rather than
// guessing a family from the underscores in a name.
func TestPresetWhitelistWithoutRegistryMatchesExactNamesOnly(t *testing.T) {
	reg := registryWith(t, "goal_create", "goal_get")
	tool := NewDelegateTool(DelegateConfig{SessionRunner: &capturingRunner{}, Registry: reg})

	excluded := tool.excludedTools([]string{"goal"}, true)
	slices.Sort(excluded)
	if want := []string{"goal_create", "goal_get"}; !slices.Equal(excluded, want) {
		t.Fatalf("excluded = %v, want %v", excluded, want)
	}
	if excluded := tool.excludedTools([]string{"goal_get"}, true); !slices.Equal(excluded, []string{"goal_create"}) {
		t.Fatalf("exact-name whitelist excluded = %v, want [goal_create]", excluded)
	}
}
