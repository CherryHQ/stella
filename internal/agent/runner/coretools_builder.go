package runner

import (
	"runtime"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// CoreToolsBuilderWithBoxsh creates a CoreToolsBuilder that uses boxsh-backed
// adapters on Linux/macOS when a backend is available, falling back to the
// provided delegate on Windows or when sandbox is disabled.
func CoreToolsBuilderWithBoxsh(delegate CoreToolsBuilder) CoreToolsBuilder {
	return func(bc plugintools.BuildContext) []tools.Tool {
		// On Windows or when no backend available, use delegate.
		if runtime.GOOS == "windows" || bc.Backend == nil {
			return delegate(bc)
		}

		// On Linux/macOS with backend, use boxsh-backed adapters.
		return buildBoxshCoreTools(bc)
	}
}

// buildBoxshCoreTools creates core tools using the boxsh backend.
func buildBoxshCoreTools(bc plugintools.BuildContext) []tools.Tool {
	if bc.Backend == nil {
		return nil
	}

	var tools []tools.Tool

	// Bash tool.
	tools = append(tools, boxshclient.NewBashAdapter(bc.Backend, bc.ToolsBinDir))

	// Read tool.
	tools = append(tools, boxshclient.NewReadAdapter(bc.Backend))

	// Write tool.
	tools = append(tools, boxshclient.NewWriteAdapter(bc.Backend))

	// Edit tool.
	tools = append(tools, boxshclient.NewEditAdapter(bc.Backend))

	return tools
}
