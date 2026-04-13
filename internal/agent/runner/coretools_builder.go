package runner

import (
	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// CoreToolsBuilderWithSandbox creates a CoreToolsBuilder that only returns
// host-backed sandbox tools. It fails closed by returning no tools when there
// is no active sandbox host.
func CoreToolsBuilderWithSandbox(_ CoreToolsBuilder, session *runnerSession) CoreToolsBuilder {
	return func(bc plugintools.BuildContext) []tools.Tool {
		return buildSandboxCoreTools(session, bc)
	}
}

// buildSandboxCoreTools creates core tools using the active sandbox host.
func buildSandboxCoreTools(session *runnerSession, bc plugintools.BuildContext) []tools.Tool {
	if session == nil || session.Session() == nil || session.Session().Host() == nil {
		return nil
	}
	return sandbox.NewCoreTools(session.Session().Host(), bc.ToolsBinDir)
}
