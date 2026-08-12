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
	if store == nil {
		return pkgplugins.SystemPromptSection{}, nil
	}

	svc := NewService(store, build.StellaHome)
	vc := pkgplugins.SkillViewContext{
		UserID:            build.UserID,
		AgentID:           build.AgentID,
		DisabledSkillRefs: append([]string(nil), build.DisabledSkillRefs...),
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

	systemSkills := promptSystemSkills(all)

	var content strings.Builder
	if len(systemSkills) > 0 {
		content.WriteString("System skills are listed below. Load one with the skills tool before following its instructions: action=\"load\", name=\"<skill-name>\". ")
	} else {
		content.WriteString("Search installed skills before loading skill instructions. ")
	}
	content.WriteString("For project, user, or agent skills not listed here, call the skills tool with action=\"search_installed\" and a compact task-oriented query, then load the selected skill with action=\"load\", name=\"<skill-name>\". ")
	content.WriteString("To load a specific file within a selected skill, use action=\"load\", name=\"<skill-name>\", path=\"<relative-path>\" ")
	content.WriteString("(path is relative to the skill root, e.g. \"references/api.md\").")
	if len(systemSkills) > 0 {
		content.WriteString("\n\n<system_skills>\n")
		for _, skill := range systemSkills {
			content.WriteString("  <skill>\n")
			content.WriteString("    <name>")
			content.WriteString(escapeXML(skill.Name))
			content.WriteString("</name>\n")
			content.WriteString("    <description>")
			content.WriteString(escapeXML(skill.Description))
			content.WriteString("</description>\n")
			content.WriteString("  </skill>\n")
		}
		content.WriteString("</system_skills>")
	}

	return pkgplugins.SystemPromptSection{
		Title:   "Skills",
		Content: content.String(),
	}, nil
}

func promptSystemSkills(skills []pkgplugins.Skill) []pkgplugins.Skill {
	out := make([]pkgplugins.Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.Scope != "system" || skill.Status == SkillStatusDeprecated || skill.DisableModelInvocation {
			continue
		}
		out = append(out, skill)
	}
	return out
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
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
		if skill.Scope != "system" && skill.Scope != "builtin" {
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
	if owner != "" {
		return owner
	}
	nested, _ := meta["metadata"].(map[string]any)
	owner, _ = nested["owner_plugin"].(string)
	return owner
}
