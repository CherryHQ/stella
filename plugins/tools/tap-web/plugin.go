package tapweb

import (
	"context"

	"github.com/vaayne/anna/internal/builddeps"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "tool/tap-web"

const promptContent = `Use the ` + "`tap`" + ` CLI together with the builtin ` + "`tap-web`" + ` skill for web lookup, readable extraction, browser automation, and network capture.

- Start with the ` + "`tap-web`" + ` skill guidance before inventing raw tap commands.
- Prefer lighter fetch/extract flows first; escalate to browser automation only when the page actually needs JavaScript, login state, or interaction.
- Site-specific notes live under ` + "`$XDG_CONFIG_HOME/tap/site-notes/`" + ` when tap uses its default config home.`

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "tool",
			Name:         "tap-web",
			DisplayName:  "Tap Web",
			Description:  "Web browsing skill powered by the tap CLI — fetch pages, run browser automation, and capture network traffic.",
			AdminVisible: true,
			Capabilities: []string{pkgplugins.CapabilityBinary, pkgplugins.CapabilityPrompt},
		})
		host.AddBinary(pkgplugins.BinarySpec{
			PluginID: PluginID,
			Name:     "tap",
			Repo:     "vaayne/tap",
			Version:  "0.4.4",
			AssetTemplates: map[string]pkgplugins.BinaryAsset{
				"darwin-amd64":  {File: "tap_{version}_darwin_amd64.tar.gz"},
				"darwin-arm64":  {File: "tap_{version}_darwin_arm64.tar.gz"},
				"linux-amd64":   {File: "tap_{version}_linux_amd64.tar.gz"},
				"linux-arm64":   {File: "tap_{version}_linux_arm64.tar.gz"},
				"windows-amd64": {File: "tap_{version}_windows_amd64.zip"},
				"windows-arm64": {File: "tap_{version}_windows_arm64.zip"},
			},
		})
		host.AddBundledSkill(pkgplugins.BundledSkillSpec{
			PluginID: PluginID,
			Name:     "tap-web",
			Sync:     builddeps.SyncTapWebBundledSkill,
		})
		host.AddSystemPrompt(pkgplugins.SystemPromptSpec{
			PluginID: PluginID,
			Name:     "tap-web",
			Build: func(_ context.Context, _ pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
				return pkgplugins.SystemPromptSection{Title: "Tap Web", Content: promptContent}, nil
			},
		})
	}))
}
