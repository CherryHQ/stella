package resources

import (
	"fmt"
	"slices"
)

// builtinSkillPluginRequirements is the release-owned, one-hop dependency
// declaration for builtin Skills. Dependencies are an all-of gate for public
// exposure; optional, alternative, and versioned dependencies require a new
// model rather than adding more entries here.
var builtinSkillPluginRequirements = map[string][]string{
	"python-script": {"tool/uv"},
	"web":           {"tool/bun"},
}

func builtinSkillRequiresPluginIDs(name string) []string {
	return slices.Clone(builtinSkillPluginRequirements[name])
}

func validateBuiltinSkillRequirements(skill BuiltinSkillDescriptor) error {
	want := builtinSkillPluginRequirements[skill.Name]
	if !slices.Equal(skill.RequiresPluginIDs, want) {
		return fmt.Errorf("builtin skill %q has requirements %v, want %v", skill.Name, skill.RequiresPluginIDs, want)
	}
	seen := make(map[string]struct{}, len(skill.RequiresPluginIDs))
	for _, id := range skill.RequiresPluginIDs {
		if id == "" {
			return fmt.Errorf("builtin skill %q has empty required plugin ID", skill.Name)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("builtin skill %q repeats required plugin %q", skill.Name, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
