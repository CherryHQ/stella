package pluginhooks

import (
	"io"
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/pkg/hooks"
)

// BuildContext carries configuration for hook construction.
type BuildContext struct {
	ToolsBinDir string // path to embedded tool binaries
}

// Factory creates a new hook plugin instance.
type Factory func(BuildContext) (hooks.HookPlugin, error)

// Registration holds a factory for a hook plugin.
type Registration struct {
	Factory Factory
}

var (
	mu       sync.RWMutex
	registry = map[string]Registration{}
)

// Register registers a hook plugin factory by name.
// Typically called from init() in each plugin hook package.
func Register(name string, reg Registration) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = reg
}

// Names returns all registered hook plugin names in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// BuildEnabled builds hook plugins that the caller considers enabled.
// The enabled callback returns true for plugins that should be instantiated —
// typically by consulting the config store.
// Callers (NewHookSet) handle priority sorting.
func BuildEnabled(bc BuildContext, enabled func(name string) bool) []hooks.HookPlugin {
	mu.RLock()
	regs := make(map[string]Registration, len(registry))
	for name, reg := range registry {
		regs[name] = reg
	}
	mu.RUnlock()

	var result []hooks.HookPlugin
	for name, reg := range regs {
		if !enabled(name) {
			slog.Debug("plugin hook disabled", "name", name)
			continue
		}
		p, err := reg.Factory(bc)
		if err != nil {
			slog.Warn("failed to build hook plugin", "name", name, "error", err)
			continue
		}
		result = append(result, p)
	}

	return result
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
