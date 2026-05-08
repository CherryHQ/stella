package plugintools

import (
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	Paths   pkgplugins.ToolPaths
	Runtime sandbox.Session
}
