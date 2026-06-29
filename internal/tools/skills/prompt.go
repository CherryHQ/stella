package skills

import (
	"context"
	"encoding/json"
	"strings"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func BuildPromptSection(ctx context.Context, build pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
	var store pkgplugins.SkillStore
	if build.SkillStore != nil {
		store = build.SkillStore
	} else if build.Platform != nil {
		store = build.Platform.SkillStore()
	}

	svc := NewService(store, build.StellaHome)
	vc := pkgplugins.SkillViewContext{
		UserID:  build.UserID,
		AgentID: build.AgentID,
	}

	merged, err := svc.ListMerged(ctx, vc, build.ProjectRoot)
	if err != nil {
		return pkgplugins.SystemPromptSection{}, err
	}

	// Apply plugin visibility filtering.
	all := make([]pkgplugins.Skill, 0, len(merged))
	for _, rs := range merged {
		all = append(all, rs.Skill)
	}
	all = filterVisibleSkills(all, build)

	if len(all) == 0 {
		return pkgplugins.SystemPromptSection{}, nil
	}

	var content strings.Builder
	content.WriteString("Search installed skills before loading skill instructions. ")
	content.WriteString("Call the skills tool with action=\"search_installed\" and a compact task-oriented query. ")
	content.WriteString("Then load the selected skill with action=\"load\", name=\"<skill-name>\". ")
	content.WriteString("To load a specific file within a selected skill, use action=\"load\", name=\"<skill-name>\", path=\"<relative-path>\" ")
	content.WriteString("(path is relative to the skill root, e.g. \"references/api.md\").")

	return pkgplugins.SystemPromptSection{
		Title:   "Skills",
		Content: content.String(),
	}, nil
}

func filterVisibleSkills(skills []pkgplugins.Skill, build pkgplugins.SystemPromptContext) []pkgplugins.Skill {
	if len(skills) == 0 {
		return nil
	}
	registered := make(map[string]struct{}, len(build.RegisteredPluginIDs))
	for _, id := range build.RegisteredPluginIDs {
		registered[id] = struct{}{}
	}
	enabled := make(map[string]struct{}, len(build.EnabledPluginIDs))
	for _, id := range build.EnabledPluginIDs {
		enabled[id] = struct{}{}
	}

	out := make([]pkgplugins.Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.Scope != "system" {
			out = append(out, skill)
			continue
		}
		owner := ownerPlugin(skill.Metadata)
		if owner == "" || skill.Name == "stella" {
			out = append(out, skill)
			continue
		}
		if _, ok := registered[owner]; !ok {
			out = append(out, skill)
			continue
		}
		if _, ok := enabled[owner]; ok {
			out = append(out, skill)
		}
	}
	return out
}

func ownerPlugin(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ""
	}
	owner, _ := meta["owner_plugin"].(string)
	return owner
}
