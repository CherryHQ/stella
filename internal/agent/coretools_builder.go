package agent

import (
	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/vision"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// buildSandboxCoreTools creates core tools using the active sandbox session.
func buildSandboxCoreTools(session pkgsandbox.Session, bc pkgplugins.ToolBuildContext, sessionSecretValues *agentsandbox.SessionSecretValues, visionSvc *vision.Service) []pkgtools.Tool {
	if session == nil {
		return nil
	}
	return agentsandbox.NewTools(session, bc.Paths.ToolsBinDir, bc.Paths.ProjectRoot, sessionSecretValues, visionSvc)
}
