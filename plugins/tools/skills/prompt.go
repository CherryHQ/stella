package skills

import (
	"context"
	"strings"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func buildPromptSection(_ context.Context, build pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
	skills := VisibleSkills(LoadSkills(
		build.AnnaHome,
		build.Workspace,
		build.Cwd,
		userSkillsDir(build.UserDataDir),
	))
	if len(skills) == 0 {
		return pkgplugins.SystemPromptSection{}, nil
	}

	var content strings.Builder
	content.WriteString(`Load a skill with the skills tool: action="load", name="<skill-name>". Resolve relative paths against the base_dir returned by load. Draft skills can be enabled with action="patch", name="<skill-name>", status="active".`)
	content.WriteString("\n\n<available_skills>\n")
	for _, skill := range skills {
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
