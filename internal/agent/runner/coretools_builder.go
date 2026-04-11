package runner

import (
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// CoreToolsBuilderWithSandbox creates a CoreToolsBuilder that uses sandbox
// adapters when a sandbox backend is available, falling back to the provided
// delegate otherwise.
func CoreToolsBuilderWithSandbox(delegate CoreToolsBuilder, backend sandboxBackend) CoreToolsBuilder {
	return func(bc plugintools.BuildContext) []tools.Tool {
		// Without a core sandbox backend, use delegate.
		if backend == nil || backend.Boxsh() == nil {
			if delegate != nil {
				return delegate(bc)
			}
			return nil
		}

		// With backend, use sandbox-backed adapters.
		return buildSandboxCoreTools(backend, bc)
	}
}

// buildSandboxCoreTools creates core tools using the active sandbox backend.
func buildSandboxCoreTools(backend sandboxBackend, bc plugintools.BuildContext) []tools.Tool {
	boxsh := backend.Boxsh()
	if boxsh == nil {
		return nil
	}

	var tools []tools.Tool

	// Bash tool.
	tools = append(tools, boxshclient.NewBashAdapter(boxsh, bc.ToolsBinDir))

	// Read tool.
	tools = append(tools, boxshclient.NewReadAdapter(boxsh))

	// Write tool.
	tools = append(tools, boxshclient.NewWriteAdapter(boxsh))

	// Edit tool.
	tools = append(tools, boxshclient.NewEditAdapter(boxsh))

	return tools
}
