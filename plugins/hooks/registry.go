package pluginhooks

import (
	"io"
	"log/slog"

	"github.com/CherryHQ/stella/pkg/hooks"
)

// BuildContext carries configuration for hook construction.
type BuildContext struct {
	ToolsBinDir string // path to embedded tool binaries
}

// CloseHookPlugins closes any hook plugins that implement io.Closer.
// Call during graceful shutdown or before replacing hook instances on reload.
func CloseHookPlugins(plugins []hooks.HookPlugin) {
	for _, p := range plugins {
		if c, ok := p.(io.Closer); ok {
			if err := c.Close(); err != nil {
				slog.Warn("failed to close hook plugin", "name", p.Name(), "error", err)
			}
		}
	}
}
