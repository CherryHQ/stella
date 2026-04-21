package plugintools

import (
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/sandbox"
)

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	Paths   pkgplugins.ToolPaths
	Runtime sandbox.Session
}
