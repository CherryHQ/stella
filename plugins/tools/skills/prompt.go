package skills

import (
	"context"
	"strings"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// alwaysOnBuiltinSkill is the one builtin skill visible to every agent
// regardless of the per-agent enabled_builtin_skills list. It carries anna's
// self-knowledge.
const alwaysOnBuiltinSkill = "anna"

func BuildPromptSection(ctx context.Context, build pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
	if build.Platform == nil {
		return pkgplugins.SystemPromptSection{}, nil
	}
	store := build.Platform.SkillStore()
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

	// Filter system-scope builtin skills to the per-agent allowlist (plus the
	// always-on "anna" self-knowledge skill). Agent/user/project skills pass
	// through — they are already owned, not part of the shared catalog.
	allowedBuiltin := make(map[string]bool, len(build.EnabledBuiltinSkills)+1)
	allowedBuiltin[alwaysOnBuiltinSkill] = true
	for _, name := range build.EnabledBuiltinSkills {
		allowedBuiltin[name] = true
	}
	filtered := make([]pkgplugins.Skill, 0, len(dbSkills))
	for _, s := range dbSkills {
		if s.Scope == "system" && !allowedBuiltin[s.Name] {
			continue
		}
		filtered = append(filtered, s)
	}

	// Merge project skills from filesystem (project > user > agent > system precedence).
	projSkills, _, _ := ListProjectSkills(build.ProjectRoot)
	projNames := make(map[string]bool, len(projSkills))
	for _, s := range projSkills {
		projNames[s.Name] = true
	}
	all := make([]pkgplugins.Skill, 0, len(projSkills)+len(filtered))
	all = append(all, projSkills...)
	for _, s := range filtered {
		if !projNames[s.Name] {
			all = append(all, s)
		}
	}

	if len(all) == 0 {
		return pkgplugins.SystemPromptSection{}, nil
	}

	var content strings.Builder
	content.WriteString(`Load a skill with the skills tool: action="load", name="<skill-name>". To read a referenced file under a skill, pass action="load", name="<skill-name>", path="references/api.md". Draft skills can be enabled with action="patch", name="<skill-name>", status="active".`)
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
