package plugintools

import pkgplugins "github.com/vaayne/anna/pkg/plugins"

// BuildContext carries per-session configuration for tool construction.
// Execution uses UserRoot + WorkDir. AnnaHome, AgentRoot, and ProjectRoot are
// discovery/config inputs and do not redefine the writable root.
type BuildContext struct {
	WorkDir     string // runtime working directory for tool execution; always inside UserRoot
	ProjectRoot string // optional project-scoped discovery root for local/project-attached runs
	UserRoot    string // runtime writable root and sandbox HOME
	AnnaHome    string // anna home directory for builtin assets and shared state
	AgentRoot   string // agent-scoped discovery root, not the sandbox writable root
	ToolsBinDir string // path to anna tools bin directory (prepended to PATH)
	Runtime     pkgplugins.ToolRuntime
}
