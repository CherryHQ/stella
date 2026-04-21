package runner

import (
	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// buildSandboxCoreTools creates core tools using the active sandbox session.
func buildSandboxCoreTools(session *runnerSession, bc plugintools.BuildContext) []tools.Tool {
	if session == nil || session.Session() == nil {
		return nil
	}
	return sandbox.NewCoreTools(session.Session(), bc.Paths.ToolsBinDir, bc.Paths.ProjectRoot)
}
