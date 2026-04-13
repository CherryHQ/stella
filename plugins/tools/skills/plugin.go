package skills

import (
	"path/filepath"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

const PluginID = "tool/skills"

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:          PluginID,
			Kind:        "tool",
			Name:        "skills",
			DisplayName: "Skills",
			Description: "Manage local agent skills.",
			Capabilities: []string{
				pkgplugins.CapabilityTool,
				pkgplugins.CapabilityPrompt,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    PluginID,
			Name:        "skills",
			Description: "Manage local agent skills.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return NewTool(ctx.AnnaHome, ctx.Workspace, ctx.WorkDir, userSkillsDir(ctx.UserDataDir), ctx.Host, ctx.HomeDir), nil
			},
		})
		host.AddSystemPrompt(pkgplugins.SystemPromptSpec{
			PluginID: PluginID,
			Name:     "skills",
			Required: true,
			Build:    buildPromptSection,
		})
	}))
}

func userSkillsDir(userDataDir string) string {
	if userDataDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(userDataDir), ".agents", "skills")
}

func SkillsDefinition() tools.Definition {
	return pkgskillsToolDefinition()
}
