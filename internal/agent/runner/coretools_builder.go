package runner

import (
	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// CoreToolsBuilderWithSandbox creates a CoreToolsBuilder that uses host-backed
// tools when a sandbox session is available, falling back to the provided
// delegate otherwise.
func CoreToolsBuilderWithSandbox(delegate CoreToolsBuilder, session *runnerSession) CoreToolsBuilder {
	return func(bc plugintools.BuildContext) []tools.Tool {
		if session == nil || session.Session() == nil || session.Session().Host() == nil {
			if delegate != nil {
				return delegate(bc)
			}
			return nil
		}
		return buildSandboxCoreTools(session, bc)
	}
}

// buildSandboxCoreTools creates core tools using the active sandbox host.
func buildSandboxCoreTools(session *runnerSession, bc plugintools.BuildContext) []tools.Tool {
	host := session.Session().Host()
	if host == nil {
		return nil
	}
	return sandbox.NewCoreTools(host, bc.ToolsBinDir)
}
