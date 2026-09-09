package plugin

import (
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func resolveFileResourceConflicts(resources []FileResource) {
	standalone := map[string]bool{}
	for _, resource := range resources {
		if resource.Key.Kind == ResourceSkill {
			standalone[resource.Key.Name] = true
		}
	}
	skills := map[string][]int{}
	aliases := map[string][]int{}
	env := map[string][]int{}
	for i, resource := range resources {
		if resource.Disabled || resource.Package == nil {
			continue
		}
		for _, skill := range resource.Skills {
			if !standalone[skill.Name] {
				skills[skill.Name] = append(skills[skill.Name], i)
			}
		}
		if extension := resource.Package.Extension; extension != nil {
			for _, binary := range extension.Binaries {
				aliases[binary.Name] = append(aliases[binary.Name], i)
			}
			for _, value := range extension.SessionEnv {
				env[value.EnvVar] = append(env[value.EnvVar], i)
			}
			for _, oauth := range extension.OAuth {
				for _, binding := range oauth.Bindings {
					if binding.EnvVar != "" {
						env[binding.EnvVar] = append(env[binding.EnvVar], i)
					}
				}
			}
		}
	}
	for i := range resources {
		resource := &resources[i]
		if resource.Key.Kind != ResourcePlugin {
			continue
		}
		kept := resource.Skills[:0]
		for _, skill := range resource.Skills {
			if standalone[skill.Name] {
				continue
			}
			if len(skills[skill.Name]) > 1 {
				resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.skill_conflict", Path: skill.Path, Message: "multiple selected packages provide this Skill"})
				continue
			}
			kept = append(kept, skill)
		}
		resource.Skills = kept
	}
	for _, declarations := range []map[string][]int{aliases, env} {
		for _, owners := range declarations {
			if len(owners) < 2 {
				continue
			}
			for _, owner := range owners {
				resource := &resources[owner]
				resource.Disabled = true
				resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.requirement_conflict", Message: "selected packages declare conflicting execution requirements"})
			}
		}
	}
}
