package tools

import (
	"context"
	"fmt"
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

// pluginSkillFuncs maps plugin IDs to their skill extraction functions.
// Plugins register themselves via RegisterPluginSkill in init().
var pluginSkillFuncs = map[string]func(skillsDir string) error{}

// RegisterPluginSkill registers a skill extraction function for a plugin.
// Called from plugin init() functions.
func RegisterPluginSkill(pluginID string, fn func(skillsDir string) error) {
	pluginSkillFuncs[pluginID] = fn
}

// EnsurePluginSkill extracts the embedded skill for a plugin into annaHome/skills/.
// Synchronous — skill files are small and must be present before the next agent run.
// Safe to call for any plugin ID — no-op if the plugin has no embedded skill.
func EnsurePluginSkill(pluginID, annaHome string) error {
	fn, ok := pluginSkillFuncs[pluginID]
	if !ok {
		return nil
	}
	skillsDir := filepath.Join(annaHome, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}
	return fn(skillsDir)
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
