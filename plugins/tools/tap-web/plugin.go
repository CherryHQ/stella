package tapweb

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"

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
			PostInstall: installSkill,
		})
	}))
}

func installSkill(ctx context.Context, binPath, annaHome string, logger *slog.Logger) {
	skillPath := filepath.Join(annaHome, "skills", "tap-web")
	if err := exec.CommandContext(ctx, binPath, "skill", "install", "--path", skillPath).Run(); err != nil {
		logger.Error("failed to install tap-web skill", "error", err)
	} else {
		logger.Info("tap-web skill installed", "path", skillPath)
	}
}
