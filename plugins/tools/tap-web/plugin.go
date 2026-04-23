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
			Capabilities: []string{pkgplugins.CapabilityPrompt},
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
