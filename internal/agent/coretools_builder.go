package agent

import (
	"github.com/CherryHQ/stella/internal/tools/coretools"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
	plugintools "github.com/CherryHQ/stella/plugins/tools"
)

// buildSandboxCoreTools creates core tools using the active sandbox session.
func buildSandboxCoreTools(session pkgsandbox.Session, bc plugintools.BuildContext) []tools.Tool {
	if session == nil {
		return nil
	}
	return coretools.New(session, bc.Paths.ToolsBinDir, bc.Paths.ProjectRoot)
}
