package skills

import (
	"os"
	"path/filepath"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

const PluginID = "tool/skills"

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
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
		host.Registry().RegisterTool(pkgplugins.ToolRegistration{
			PluginID:    PluginID,
			Name:        "skills",
			Description: "Manage local agent skills.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				cwd, _ := os.Getwd()
				return NewTool(ctx.AnnaHome, ctx.Workspace, cwd, userSkillsDir(ctx.UserDataDir)), nil
			},
		})
		host.Registry().RegisterSystemPrompt(pkgplugins.SystemPromptRegistration{
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
