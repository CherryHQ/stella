package tools

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
)

// BinDir returns the tools binary directory path within annaHome.
func BinDir(annaHome string) string {
	return filepath.Join(annaHome, "bin")
}

// ToolPath returns the full path to a named downloadable tool, or empty if not installed.
func ToolPath(annaHome, name string) string {
	p := filepath.Join(BinDir(annaHome), name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// pluginToolMap maps plugin IDs to the tool name they require.
var pluginToolMap = map[string]string{
	"hook/rtk": "rtk",
}

// EnsurePluginTool downloads the tool binary required by a plugin (if any)
// in the background. Safe to call for any plugin ID — it's a no-op if the
// plugin doesn't require a downloadable tool.
func EnsurePluginTool(_ context.Context, pluginID, annaHome string, logger *slog.Logger) {
	toolName, ok := pluginToolMap[pluginID]
	if !ok {
		return
	}
	tool := FindTool(toolName)
	if tool == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	binDir := BinDir(annaHome)
	go func() {
		if err := Download(context.Background(), tool, binDir, Platform()); err != nil {
			logger.Error("failed to download plugin tool", "plugin", pluginID, "tool", toolName, "error", err)
		} else {
			logger.Info("plugin tool downloaded", "plugin", pluginID, "tool", toolName)
		}
	}()
}
