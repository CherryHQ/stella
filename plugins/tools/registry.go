package plugintools

import pkgplugins "github.com/vaayne/anna/pkg/plugins"

// ExecutionContext carries runtime execution paths for tool construction.
// WorkDir is always inside UserRoot, and ToolsBinDir is prepended to PATH when needed.
type ExecutionContext struct {
	WorkDir     string // runtime working directory for tool execution; always inside UserRoot
	UserRoot    string // runtime writable root and sandbox HOME
	ToolsBinDir string // path to anna tools bin directory (prepended to PATH)
}

// DiscoveryContext carries filesystem discovery scope for tool construction.
// These paths are used to locate assets/config and do not redefine the writable root.
type DiscoveryContext struct {
	AnnaHome    string // anna home directory for builtin assets and shared state
	AgentRoot   string // agent-scoped discovery root, not the sandbox writable root
	ProjectRoot string // optional project-scoped discovery root for local/project-attached runs
	UserRoot    string // user-scoped discovery root for per-user assets
}

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	Execution ExecutionContext
	Discovery DiscoveryContext
	Runtime   pkgplugins.ToolRuntime
}
