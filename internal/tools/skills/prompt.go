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
	content.WriteString("Load a skill via CLI before following its instructions: `stella skill load <name>`. " +
		"The output includes the SKILL.md content plus a list of referenced files. " +
		"Load every referenced file with `stella skill load <name> --path <file>` before acting.")
	content.WriteString("\n\n<available_skills>\n")
	for _, skill := range all {
		content.WriteString("  <skill>\n")
		content.WriteString("    <name>")
		content.WriteString(escapeXML(skill.Name))
		content.WriteString("</name>\n")
		content.WriteString("    <description>")
		content.WriteString(escapeXML(skill.Description))
		content.WriteString("</description>\n")
		content.WriteString("    <status>")
		content.WriteString(escapeXML(skill.Status))
		content.WriteString("</status>\n")
		content.WriteString("  </skill>\n")
	}
	content.WriteString("</available_skills>")

	return pkgplugins.SystemPromptSection{
		Title:   "Skills",
		Content: content.String(),
	}, nil
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
