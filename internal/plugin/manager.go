package plugin

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaayne/anna/internal/config"
)

// Manager discovers, loads, and manages JS plugins.
type Manager struct {
	registry *Registry
	plugins  []*JSPlugin
	logger   *slog.Logger
}

// NewManager creates a plugin manager.
func NewManager(logger *slog.Logger, builtinToolNames []string) *Manager {
	return &Manager{
		registry: NewRegistry(builtinToolNames),
		logger:   logger,
	}
}

// Registry returns the plugin registry.
func (m *Manager) Registry() *Registry {
	return m.registry
}

// LoadAll loads JS plugins from config. Best-effort: logs warnings for failures
// and continues loading remaining plugins.
func (m *Manager) LoadAll(configs []config.PluginConfig) {
	for _, cfg := range configs {
		path := ExpandPath(cfg.Path)
		if !isJSPlugin(path) {
			m.logger.Warn("unsupported plugin type (only .js supported)", "path", path)
			continue
		}
		m.loadJS(path, cfg.Config)
	}
}

// Close shuts down all plugins in reverse order.
func (m *Manager) Close() error {
	var lastErr error
	for i := len(m.plugins) - 1; i >= 0; i-- {
		if err := m.plugins[i].Close(); err != nil {
			m.logger.Warn("plugin close error", "plugin", m.plugins[i].Name(), "error", err)
			lastErr = err
		}
	}
	return lastErr
}

func (m *Manager) loadJS(path string, cfg map[string]any) {
	p, err := LoadJS(path, cfg, m.registry, m.logger)
	if err != nil {
		m.logger.Warn("failed to load JS plugin", "path", path, "error", err)
		return
	}
	m.plugins = append(m.plugins, p)
}

// isJSPlugin checks if the path points to a .js file.
func isJSPlugin(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && strings.HasSuffix(path, ".js")
}

// ExpandPath expands ~ to the user's home directory.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
