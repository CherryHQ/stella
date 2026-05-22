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

	vc := pkgplugins.SkillViewContext{
		UserID:  build.UserID,
		AgentID: build.AgentID,
	}

	dbSkills, err := store.List(ctx, vc)
	if err != nil {
		return pkgplugins.SystemPromptSection{}, err
	}
	dbSkills = filterVisibleSkills(dbSkills, build)

	// System-scope skills are always available to every agent. Agent/user/project
	// skills keep their existing visibility rules.

	// Merge project skills from filesystem (project > user > agent > system precedence).
	projSkills, _, _ := ListProjectSkills(build.ProjectRoot)
	projNames := make(map[string]bool, len(projSkills))
	for _, s := range projSkills {
		projNames[s.Name] = true
	}
	all := make([]pkgplugins.Skill, 0, len(projSkills)+len(dbSkills))
	all = append(all, projSkills...)
	for _, s := range dbSkills {
		if !projNames[s.Name] {
			all = append(all, s)
		}
	}

	if len(all) == 0 {
		return pkgplugins.SystemPromptSection{}, nil
	}

	var content strings.Builder
	content.WriteString(`Load a skill by running "stella skills load <skill-name>" via the bash tool and reading the returned file. To read a referenced file under a skill, run "stella skills load <skill-name> --path references/api.md". Draft skills can be enabled with "stella skills patch <skill-name> --status active".`)
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
