package plugin

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/plugin/jsrt"
	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

// Manager discovers, loads, and manages plugins.
type Manager struct {
	registry *Registry
	plugins  []pluginapi.Plugin
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

// LoadAll loads plugins from config. Best-effort: logs warnings for failures,
// continues loading remaining plugins.
func (m *Manager) LoadAll(configs []config.PluginConfig) error {
	// Load compiled-in Go plugin factories — only those with a matching config entry.
	for _, f := range pluginapi.Factories() {
		cfg, ok := findPluginConfig(configs, f.Name)
		if !ok {
			m.logger.Debug("skipping unconfigured Go plugin", "plugin", f.Name)
			continue
		}
		m.loadGoFactory(f, cfg)
	}

	// Load file-based plugins (JS extensions).
	for _, cfg := range configs {
		path := expandPath(cfg.Path)
		kind := detectKind(path)
		switch kind {
		case "js":
			m.loadJS(path, cfg.Config)
		case "go":
			// Go plugins are loaded via factory registry (compiled-in), not
			// from files at runtime. Skip here -- they were already loaded above.
			continue
		default:
			m.logger.Warn("unknown plugin type", "path", path)
		}
	}

	return nil
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
	p, err := jsrt.LoadJS(path, cfg, m.registry, m.logger)
	if err != nil {
		m.logger.Warn("failed to load JS plugin", "path", path, "error", err)
		return
	}
	m.plugins = append(m.plugins, p)
}

func (m *Manager) loadGoFactory(f pluginapi.Factory, cfg map[string]any) {
	p, err := f.New(cfg)
	if err != nil {
		m.logger.Warn("failed to create plugin", "plugin", f.Name, "error", err)
		return
	}

	pctx := pluginapi.Context{
		Config: cfg,
		Logger: m.logger.With("plugin", f.Name),
		RegisterTool: func(t pluginapi.Tool) error {
			return m.registry.RegisterTool(t)
		},
		OnEvent: func(kind pluginapi.EventKind, fn pluginapi.HookFunc) {
			m.registry.RegisterHook(kind, fn)
		},
	}

	if err := p.Init(pctx); err != nil {
		m.logger.Warn("plugin init failed", "plugin", f.Name, "error", err)
		return
	}

	m.plugins = append(m.plugins, p)
	m.logger.Info("plugin loaded", "plugin", f.Name)
}

// findPluginConfig finds config for a named plugin in the config list.
// Returns the config map and true if found, or nil and false if not.
func findPluginConfig(configs []config.PluginConfig, name string) (map[string]any, bool) {
	for _, cfg := range configs {
		base := filepath.Base(cfg.Path)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if base == name {
			return cfg.Config, true
		}
	}
	return nil, false
}

// detectKind determines plugin type from path.
func detectKind(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if !info.IsDir() && strings.HasSuffix(path, ".js") {
		return "js"
	}
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			return "go"
		}
	}
	return ""
}

// expandPath expands ~ to home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
