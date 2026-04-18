package skills

import (
	"context"
	"strings"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

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
		Project: build.ProjectRoot,
	}

	all, err := store.List(ctx, vc)
	if err != nil {
		return pkgplugins.SystemPromptSection{}, err
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
