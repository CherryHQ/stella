package mise

import (
	"context"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "tool/mise"

const promptContent = `Use mise to manage runtimes, tools, and environment variables.

Install a tool:     mise use <tool>[@version]   (e.g. mise use node@22, mise use python@3.12)
Run with tool:      mise exec -- <cmd>
List installed:     mise list
Show env:           mise env
Run a task:         mise run <task>
List tasks:         mise tasks

mise.toml (project) or ~/.config/mise/config.toml (global) declares tools and env.
Prefer mise over nvm, pyenv, rbenv, or manual PATH manipulation.`

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "tool",
			Name:         "mise",
			DisplayName:  "mise",
			Description:  "Inject mise usage guidance into the system prompt and provide the mise binary.",
			AdminVisible: true,
			Capabilities: []string{pkgplugins.CapabilityPrompt},
		})
		host.AddBinary(pkgplugins.BinarySpec{
			PluginID: PluginID,
			Name:     "mise",
			Repo:     "jdx/mise",
			AssetTemplates: map[string]pkgplugins.BinaryAsset{
				"darwin-amd64":  {File: "mise-{tag}-macos-x64.tar.gz"},
				"darwin-arm64":  {File: "mise-{tag}-macos-arm64.tar.gz"},
				"linux-amd64":   {File: "mise-{tag}-linux-x64.tar.gz"},
				"linux-arm64":   {File: "mise-{tag}-linux-arm64.tar.gz"},
				"windows-amd64": {File: "mise-{tag}-windows-x64.zip"},
				"windows-arm64": {File: "mise-{tag}-windows-arm64.zip"},
			},
		})
		host.AddSystemPrompt(pkgplugins.SystemPromptSpec{
			PluginID: PluginID,
			Name:     "mise",
			Build: func(_ context.Context, _ pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
				return pkgplugins.SystemPromptSection{
					Title:   "mise",
					Content: promptContent,
				}, nil
			},
		})
	}))
}
