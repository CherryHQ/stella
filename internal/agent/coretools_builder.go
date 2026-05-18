package agent

import (
	"github.com/CherryHQ/stella/internal/tools"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// buildSandboxCoreTools creates core tools using the active sandbox session.
func buildSandboxCoreTools(session pkgsandbox.Session, bc pkgplugins.ToolBuildContext) []pkgtools.Tool {
	if session == nil {
		return nil
	}
	return tools.New(session, bc.Paths.ToolsBinDir, bc.Paths.ProjectRoot)
}
