package tapweb

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"

	"github.com/vaayne/anna/internal/tools"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const PluginID = "tool/tap-web"

func init() {
	tools.RegisterPluginPostInstall(PluginID, installSkill)

	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "tool",
			Name:         "tap-web",
			DisplayName:  "Tap Web",
			Description:  "Web browsing skill powered by the tap CLI — fetch pages, run browser automation, and capture network traffic.",
			AdminVisible: true,
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
