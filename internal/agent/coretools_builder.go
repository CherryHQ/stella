package agent

import (
	"github.com/CherryHQ/stella/internal/agent/coretools"
	"github.com/CherryHQ/stella/pkg/tools"
	plugintools "github.com/CherryHQ/stella/plugins/tools"
)

// buildSandboxCoreTools creates core tools using the active sandbox session.
func buildSandboxCoreTools(session *runnerSession, bc plugintools.BuildContext) []tools.Tool {
	if session == nil || session.Session() == nil {
		return nil
	}
	return coretools.New(session.Session(), bc.Paths.ToolsBinDir, bc.Paths.ProjectRoot)
}
