package skills

import (
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
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
				pkgplugins.CapabilityPrompt,
			},
		})
		host.AddSystemPrompt(pkgplugins.SystemPromptSpec{
			PluginID: PluginID,
			Name:     "skills",
			Required: false,
			Build:    BuildPromptSection,
		})
	}))
}
