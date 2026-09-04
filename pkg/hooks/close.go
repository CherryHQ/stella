package hooks

import (
	"io"
	"log/slog"
)

// ClosePlugins closes hook implementations that own resources.
func ClosePlugins(plugins []HookPlugin) {
	for _, plugin := range plugins {
		closer, ok := plugin.(io.Closer)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			slog.Warn("failed to close hook", "name", plugin.Name(), "error", err)
		}
	}
}
