package plugintools

import pkgplugins "github.com/vaayne/anna/pkg/plugins"

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	Paths   pkgplugins.ToolPaths
	Runtime pkgplugins.ToolRuntime
}
