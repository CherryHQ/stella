package plugintools

import (
	"github.com/vaayne/anna/internal/sandbox"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// BuildContext carries per-session configuration for tool construction.
// Build-time contexts remain sandbox-agnostic; Host is injected at execution time.
type BuildContext struct {
	WorkDir     string                    // working directory for tool execution
	UserDataDir string                    // per-user sandbox directory (empty = no sandbox)
	AnnaHome    string                    // anna home directory (e.g. ~/.anna)
	Workspace   string                    // agent workspace dir
	ToolsBinDir string                    // path to anna tools bin directory (prepended to PATH)
	Sandbox     pkgplugins.SandboxRuntime // sandbox runtime abstraction for plugins
	Host        sandbox.Host              // execution-time sandbox host for mediated filesystem/process/network access
}
