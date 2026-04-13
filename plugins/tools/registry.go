package plugintools

import "github.com/vaayne/anna/internal/sandbox"

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	WorkDir     string       // working directory for tool execution
	UserDataDir string       // optional per-user data directory used by prompts, skills, and sandbox setup
	AnnaHome    string       // anna home directory (e.g. ~/.anna)
	HomeDir     string       // user home directory for common config/skills lookup
	Workspace   string       // agent workspace dir
	ToolsBinDir string       // path to anna tools bin directory (prepended to PATH)
	Host        sandbox.Host // execution-time sandbox host for mediated filesystem/process/network access
}
