package plugintools

import (
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	WorkDir     string                     // working directory for tool execution
	UserDataDir string                     // per-user sandbox directory (empty = no sandbox)
	AnnaHome    string                     // anna home directory (e.g. ~/.anna)
	Workspace   string                     // agent workspace dir
	ToolsBinDir string                     // path to anna tools bin directory (prepended to PATH)
	Backend     *boxshclient.SharedBackend // boxsh sandbox backend (Linux/macOS only; nil on Windows or when sandbox disabled)
	Sandbox     pkgplugins.SandboxRuntime  // sandbox runtime abstraction for plugins
}
