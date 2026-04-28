package tapweb

import (
	"github.com/vaayne/anna/internal/builddeps"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "tool/tap-web"

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "tool",
			Name:         "tap-web",
			DisplayName:  "Tap Web",
			Description:  "Web browsing skill powered by the tap CLI — fetch pages, run browser automation, and capture network traffic.",
			AdminVisible: true,
		})
		host.AddBundledSkill(pkgplugins.BundledSkillSpec{
			PluginID: PluginID,
			Name:     "tap-web",
			Sync:     builddeps.SyncTapWebBundledSkill,
		})
	}))
}
