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
	"hook/rtk":     "rtk",
	"tool/tap-web": "tap",
}

// pluginPostInstall maps plugin IDs to functions run after their binary is ready.
// The function receives the binary path and annaHome so it can install additional assets.
var pluginPostInstall = map[string]func(ctx context.Context, binPath, annaHome string, logger *slog.Logger){}

// RegisterPluginPostInstall registers a post-install hook for a plugin.
// The hook runs after the plugin's binary is downloaded (or immediately at startup
// if the binary is already present). Called from plugin init() functions.
func RegisterPluginPostInstall(pluginID string, fn func(ctx context.Context, binPath, annaHome string, logger *slog.Logger)) {
	pluginPostInstall[pluginID] = fn
}

// EnsurePluginTool downloads the tool binary required by a plugin (if any)
// in the background, then runs any registered post-install hook.
// Safe to call for any plugin ID — it's a no-op if the plugin doesn't require a downloadable tool.
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
	postInstall := pluginPostInstall[pluginID]
	go func() {
		if err := Download(context.Background(), tool, binDir, Platform()); err != nil {
			logger.Error("failed to download plugin tool", "plugin", pluginID, "tool", toolName, "error", err)
			return
		}
		logger.Info("plugin tool downloaded", "plugin", pluginID, "tool", toolName)
		if postInstall != nil {
			binPath := filepath.Join(binDir, toolName)
			postInstall(context.Background(), binPath, annaHome, logger)
		}
	}()
}

// RunPluginPostInstall runs the post-install hook for a plugin if its binary is already
// present. Used at startup to refresh plugin assets for previously-enabled plugins.
// No-op if the plugin has no binary or its binary is not yet installed.
func RunPluginPostInstall(pluginID, annaHome string, logger *slog.Logger) {
	postInstall, ok := pluginPostInstall[pluginID]
	if !ok {
		return
	}
	toolName, ok := pluginToolMap[pluginID]
	if !ok {
		return
	}
	binPath := ToolPath(annaHome, toolName)
	if binPath == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	go postInstall(context.Background(), binPath, annaHome, logger)
}
